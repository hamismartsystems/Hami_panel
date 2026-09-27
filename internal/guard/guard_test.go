package guard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/link"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

type memAlert struct{ events []string }

func (m *memAlert) AddEvent(level, actor, message, meta string) error {
	m.events = append(m.events, level+"|"+actor+"|"+message)
	return nil
}

func TestAgreeRejectsForeignKeyAndPrivateLeak(t *testing.T) {
	ep := sample(link.Reality, 443)
	share, err := link.Build(ep.Inbound, ep.Clients[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(share, ep.PrivateKey) {
		t.Fatal("builder leaked the private key")
	}
	if err := agree(ep, share); err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(share, "PUB_A", "PUB_B", 1)
	if err := agree(ep, foreign); err == nil {
		t.Fatal("a foreign reality key was accepted")
	}
	if err := agree(ep, share+"PRIV_A_SECRET"); err == nil {
		t.Fatal("a leaked private key was accepted")
	}
}

func TestGuardPortOpenAndTLS(t *testing.T) {
	ln := tlsListen(t, "canary.local")
	port := ln.Addr().(*net.TCPAddr).Port
	ep := sample(link.TLS, port)
	ep.Inbound.SNI = "canary.local"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rep, err := (Watcher{Endpoint: ep, Timeout: time.Second}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || rep.Repaired {
		t.Fatalf("%+v", rep)
	}
	if !strings.Contains(rep.Link, "canary.local") || strings.Contains(rep.Link, "PUB_B") {
		t.Fatal(rep.Link)
	}
}

func TestGuardRestartsOnlyWhenPortIsClosed(t *testing.T) {
	port := freePort(t)
	ep := sample(link.None, port)
	var restarts int
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rep, err := (Watcher{
		Endpoint: ep,
		Timeout:  300 * time.Millisecond,
		Restart: func(context.Context) error {
			restarts++
			ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
			if err != nil {
				return err
			}
			t.Cleanup(func() { ln.Close() })
			go func() {
				for {
					c, err := ln.Accept()
					if err != nil {
						return
					}
					c.Close()
				}
			}()
			return nil
		},
	}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK || !rep.Repaired || restarts != 1 {
		t.Fatalf("restarts=%d report=%+v", restarts, rep)
	}
}

func TestGuardDoesNotRestartABadLink(t *testing.T) {
	ep := sample(link.Reality, 1) // invalid for dial, but link fails first
	ep.Inbound.PublicKey = ""
	var restarts int
	rep, err := (Watcher{
		Endpoint: ep,
		Restart: func(context.Context) error {
			restarts++
			return nil
		},
	}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Repaired || restarts != 0 {
		t.Fatalf("restarts=%d report=%+v", restarts, rep)
	}
}

func TestGuardAlertIsStored(t *testing.T) {
	alert := &memAlert{}
	ep := sample(link.None, freePort(t))
	_, err := (Watcher{Endpoint: ep, Alert: alert, Timeout: 200 * time.Millisecond}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(alert.events) == 0 || !strings.HasPrefix(alert.events[0], "error|guard|") {
		t.Fatalf("%v", alert.events)
	}
}

func sample(sec link.Security, port int) xray.Endpoint {
	return xray.Endpoint{
		Inbound: link.Inbound{
			ID: 1, Remark: "g", Protocol: "vless", Port: port, Host: "203.0.113.10",
			Transport: link.TCP, Security: sec,
			SNI: "www.samsung.com", PublicKey: "PUB_A", ShortID: "aa", Fingerprint: "chrome",
		},
		Clients:    []link.Client{{UUID: "11111111-1111-1111-1111-111111111111", Email: "a@hami"}},
		PrivateKey: "PRIV_A_SECRET",
		Dest:       "www.samsung.com:443",
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func tlsListen(t *testing.T, name string) net.Listener {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{name},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
			}(c)
		}
	}()
	return ln
}
