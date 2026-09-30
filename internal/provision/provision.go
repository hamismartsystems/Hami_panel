// Package provision creates inbounds and users from high-level options.
//
// Both the command line and HP-UI go through here, so a user created in the
// browser is byte-for-byte the same as one created from a terminal. Keeping
// one implementation is the whole point: the panel must never have two
// slightly different ways of building the same object.
package provision

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hamismartsystems/hami_panel/internal/reality"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
	tpl "github.com/hamismartsystems/hami_panel/internal/template"
)

// InboundOptions describes one inbound to create from a built-in template.
type InboundOptions struct {
	Template string
	Remark   string
	Host     string
	Port     int
	SNI      string
	Dest     string
	NodeID   int64
	Listen   string
	CertFile string
	KeyFile  string
	Private  bool

	// Optional overrides. Empty means "use the template value".
	PrivateKey        string
	PublicKey         string
	ShortID           string
	ObfsType          string
	ObfsPassword      string
	Alpn              string
	CongestionControl string
}

// Inbound validates the options, generates any missing Reality material and
// stores the inbound together with its server-side secrets.
//
// The Reality private key is written to the secrets table only. It never
// reaches the inbound row that link building reads from, so it cannot end up
// in a share link by accident.
func Inbound(st *store.Store, o InboundOptions) (*store.Inbound, error) {
	o.Remark = strings.TrimSpace(o.Remark)
	o.Host = strings.TrimSpace(o.Host)
	o.SNI = strings.TrimSpace(o.SNI)
	o.Dest = strings.TrimSpace(o.Dest)

	if o.Template == "" {
		return nil, errors.New("a template is required")
	}
	if o.Remark == "" {
		return nil, errors.New("a name is required")
	}
	if o.Host == "" {
		return nil, errors.New("a public host or address is required")
	}
	if o.Port < 1 || o.Port > 65535 {
		return nil, fmt.Errorf("port %d is out of range", o.Port)
	}
	t, err := tpl.ByName(o.Template)
	if err != nil {
		return nil, err
	}
	if o.Listen == "" {
		o.Listen = "0.0.0.0"
	}

	// A Reality inbound without an SNI produces a link that looks fine and
	// never connects. Refuse it here instead of shipping it to a customer.
	if t.Security == "reality" {
		if o.SNI == "" {
			return nil, errors.New("Reality needs an SNI (the domain you borrow)")
		}
		if o.Dest == "" {
			o.Dest = o.SNI + ":443"
		}
	}
	if t.Security == "tls" && o.SNI == "" {
		return nil, errors.New("TLS needs an SNI")
	}

	if o.NodeID != 0 {
		if _, err := st.GetNode(o.NodeID); err != nil {
			return nil, fmt.Errorf("no node with id %d", o.NodeID)
		}
	}
	if err := portFree(st, o.NodeID, o.Port); err != nil {
		return nil, err
	}

	privKey, pubKey, shortID := o.PrivateKey, o.PublicKey, o.ShortID
	if t.Security == "reality" {
		if privKey == "" || pubKey == "" {
			pk, pub, err := reality.GenerateKeypair()
			if err != nil {
				return nil, fmt.Errorf("reality keypair: %w", err)
			}
			if privKey == "" {
				privKey = pk
			}
			if pubKey == "" {
				pubKey = pub
			}
		}
		if shortID == "" {
			sid, err := reality.GenerateShortID(4)
			if err != nil {
				return nil, fmt.Errorf("reality short id: %w", err)
			}
			shortID = sid
		}
	}

	obfsType := pick(o.ObfsType, t.ObfsType)
	obfsPassword := pick(o.ObfsPassword, t.ObfsPassword)
	alpn := pick(o.Alpn, t.Alpn)
	congestion := pick(o.CongestionControl, t.CongestionControl)

	in := &store.Inbound{
		NodeID: o.NodeID, Remark: o.Remark, Protocol: t.Protocol, Port: o.Port,
		Host: o.Host, Transport: t.Transport, Security: t.Security, SNI: o.SNI,
		PublicKey: pubKey, ShortID: shortID, Fingerprint: t.Fingerprint,
		Path: t.Path, XHTTPMode: t.XHTTPMode, HeaderType: t.HeaderType, Flow: t.Flow,
		ObfsType: obfsType, ObfsPassword: obfsPassword, Alpn: alpn,
		CongestionControl: congestion, IsPrivate: o.Private, Enable: true,
	}
	if err := st.CreateInbound(in); err != nil {
		return nil, err
	}
	if t.Security == "reality" || privKey != "" || o.Dest != "" ||
		o.CertFile != "" || o.KeyFile != "" {
		if err := st.SetInboundSecret(store.InboundSecret{
			InboundID: in.ID, PrivateKey: privKey, Dest: o.Dest,
			Listen: o.Listen, CertFile: o.CertFile, KeyFile: o.KeyFile,
		}); err != nil {
			return nil, fmt.Errorf("store secrets: %w", err)
		}
	}
	return in, nil
}

// pick returns the override when it is set, otherwise the template value.
func pick(override, fallback string) string {
	if override != "" {
		return override
	}
	return fallback
}

// portFree refuses a port that is already taken on the same node. Two
// inbounds on one port means one of them silently never starts.
func portFree(st *store.Store, nodeID int64, port int) error {
	ins, err := st.ListInbounds()
	if err != nil {
		return err
	}
	for _, in := range ins {
		if in.NodeID == nodeID && in.Port == port {
			return fmt.Errorf("port %d is already used by %q on the same node",
				port, in.Remark)
		}
	}
	return nil
}

// UserOptions describes one subscriber to create.
type UserOptions struct {
	InboundID int64
	Email     string
	UUID      string
	QuotaGB   float64
	Days      int
	ExpireAt  *time.Time
	IPLimit   int
	SpeedKbps int
}

// User creates one client on one inbound and issues its subscription token.
func User(st *store.Store, o UserOptions) (*store.Client, error) {
	o.Email = strings.TrimSpace(o.Email)
	if o.Email == "" {
		return nil, errors.New("a user name is required")
	}
	if strings.ContainsAny(o.Email, " \t\n\r/?#") {
		return nil, errors.New("the user name cannot contain spaces or url characters")
	}
	if o.InboundID == 0 {
		return nil, errors.New("pick an inbound")
	}
	if _, err := st.GetInbound(o.InboundID); err != nil {
		return nil, fmt.Errorf("inbound %d: %w", o.InboundID, err)
	}
	if existing, err := st.GetClientByEmail(o.Email); err == nil && existing != nil {
		return nil, fmt.Errorf("user %q already exists", o.Email)
	}
	if o.QuotaGB < 0 {
		return nil, errors.New("quota cannot be negative")
	}
	if o.Days < 0 {
		return nil, errors.New("duration cannot be negative")
	}

	id := o.UUID
	if id == "" {
		id = uuid.NewString()
	}
	expires := o.ExpireAt
	if o.Days > 0 {
		t := time.Now().UTC().Add(time.Duration(o.Days) * 24 * time.Hour)
		expires = &t
	}

	c := &store.Client{
		InboundID: o.InboundID, UUID: id, Email: o.Email, Enable: true,
		TotalBytes: GBToBytes(o.QuotaGB), IPLimit: o.IPLimit,
		SpeedLimit: int64(o.SpeedKbps), ExpireAt: expires,
	}
	if err := st.CreateClient(c); err != nil {
		return nil, err
	}
	token := subs.NewToken()
	if err := st.RotateSubToken(c.ID, token); err != nil {
		return nil, fmt.Errorf("issue subscription token: %w", err)
	}
	c.SubToken = token
	return c, nil
}

// GBToBytes converts gigabytes to bytes. Zero means unlimited.
func GBToBytes(gb float64) int64 { return int64(gb * (1 << 30)) }

// BytesToGB is the inverse, for showing a stored quota in a form.
func BytesToGB(b int64) float64 { return float64(b) / (1 << 30) }
