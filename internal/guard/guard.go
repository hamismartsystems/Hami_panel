// Package guard is the panel's own check that a config is still the one
// it claims to be. It looks at this inbound only. A closed port may be
// restarted from this same inbound. A wrong key, SNI or certificate is
// reported and left alone — copying another inbound's settings is the
// failure this panel exists to avoid.
package guard

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/link"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

// Alerter records a problem or a repair. store.Store implements it.
type Alerter interface {
	AddEvent(level, actor, message, meta string) error
}

// Watcher checks one endpoint.
type Watcher struct {
	Endpoint xray.Endpoint
	// DialHost is where the port and TLS checks connect. Default 127.0.0.1,
	// so a filtered public address is not mistaken for a dead inbound.
	DialHost string
	// Restart, if set, is called at most once and only when the port is
	// closed. It must start the core from this same endpoint.
	Restart func(ctx context.Context) error
	Alert   Alerter
	Timeout time.Duration
}

// Item is one check.
type Item struct {
	Name   string
	OK     bool
	Detail string
}

// Report is the outcome. OK is true only when every check passed.
type Report struct {
	OK       bool
	Link     string
	Repaired bool
	Items    []Item
}

// Run checks the link, the port, and TLS when this inbound uses it.
func (w Watcher) Run(ctx context.Context) (Report, error) {
	ep := w.Endpoint
	if len(ep.Clients) == 0 {
		return Report{}, fmt.Errorf("guard: endpoint has no client")
	}
	timeout := w.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	host := w.DialHost
	if host == "" {
		host = "127.0.0.1"
	}
	var rep Report

	share, err := link.Build(ep.Inbound, ep.Clients[0])
	if err != nil {
		w.event("error", "link build failed", err.Error())
		rep.Items = append(rep.Items, Item{Name: "link", Detail: err.Error()})
		return rep, nil
	}
	rep.Link = share
	if err := agree(ep, share); err != nil {
		w.event("error", "link does not match this inbound", err.Error())
		rep.Items = append(rep.Items, Item{Name: "link", Detail: err.Error()})
		return rep, nil
	}
	rep.Items = append(rep.Items, Item{Name: "link", OK: true, Detail: "matches this inbound"})

	addr := net.JoinHostPort(host, strconv.Itoa(ep.Inbound.Port))
	if err := dialTCP(ctx, addr, timeout); err != nil {
		if w.Restart == nil {
			w.event("error", "port closed", addr)
			rep.Items = append(rep.Items, Item{Name: "port", Detail: "closed"})
			return rep, nil
		}
		if rerr := w.Restart(ctx); rerr != nil {
			w.event("error", "port closed and restart failed", rerr.Error())
			rep.Items = append(rep.Items, Item{Name: "port", Detail: "closed; restart failed: " + rerr.Error()})
			return rep, nil
		}
		rep.Repaired = true
		w.event("warn", "restarted core from this inbound", addr)
		if err = dialTCP(ctx, addr, timeout); err != nil {
			w.event("error", "port still closed after restart", addr)
			rep.Items = append(rep.Items, Item{Name: "port", Detail: "still closed after restart"})
			return rep, nil
		}
		rep.Items = append(rep.Items, Item{Name: "port", OK: true, Detail: "open after restart"})
	} else {
		rep.Items = append(rep.Items, Item{Name: "port", OK: true, Detail: "open"})
	}

	if ep.Inbound.Security == link.TLS || ep.Inbound.Security == link.Reality {
		if err := dialTLS(ctx, addr, ep.Inbound.SNI, timeout); err != nil {
			// A certificate or dest problem is not fixed by restarting, and
			// must not be patched from another inbound.
			w.event("error", "tls handshake failed", err.Error())
			rep.Items = append(rep.Items, Item{Name: "tls", Detail: err.Error()})
			return rep, nil
		}
		rep.Items = append(rep.Items, Item{Name: "tls", OK: true, Detail: "handshake matches " + ep.Inbound.SNI})
	} else {
		rep.Items = append(rep.Items, Item{Name: "tls", OK: true, Detail: "not required"})
	}
	rep.OK = true
	return rep, nil
}

func (w Watcher) event(level, message, meta string) {
	if w.Alert == nil {
		return
	}
	_ = w.Alert.AddEvent(level, "guard", message, meta)
}

func agree(ep xray.Endpoint, share string) error {
	if ep.PrivateKey != "" && strings.Contains(share, ep.PrivateKey) {
		return fmt.Errorf("private key leaked into the link")
	}
	in := ep.Inbound
	if in.Protocol != "" && in.Protocol != "vless" {
		if in.Host != "" && !strings.Contains(share, in.Host) {
			return fmt.Errorf("link is missing this inbound host")
		}
		return nil
	}
	parsed, err := link.Parse(share)
	if err != nil {
		return err
	}
	if parsed.UUID != ep.Clients[0].UUID || parsed.Port != in.Port || parsed.Security != in.Security {
		return fmt.Errorf("link fields do not match this inbound")
	}
	if parsed.Transport != in.Transport && !(parsed.Transport == link.TCP && in.Transport == "") {
		return fmt.Errorf("link transport does not match this inbound")
	}
	if in.Security == link.Reality && (parsed.PublicKey != in.PublicKey || parsed.SNI != in.SNI || parsed.ShortID != in.ShortID) {
		return fmt.Errorf("link reality fields do not match this inbound")
	}
	if in.Security == link.TLS && parsed.SNI != in.SNI {
		return fmt.Errorf("link sni does not match this inbound")
	}
	return nil
}

func dialTCP(ctx context.Context, addr string, timeout time.Duration) error {
	var d net.Dialer
	d.Timeout = timeout
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return conn.Close()
}

func dialTLS(ctx context.Context, addr, sni string, timeout time.Duration) error {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // the name is checked below; a private CA must not look like a dead port
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if sni != "" && !certAllows(conn.ConnectionState().PeerCertificates, sni) {
		return fmt.Errorf("certificate is not for %s", sni)
	}
	return nil
}

func certAllows(certs []*x509.Certificate, name string) bool {
	if len(certs) == 0 {
		return false
	}
	return certs[0].VerifyHostname(name) == nil
}
