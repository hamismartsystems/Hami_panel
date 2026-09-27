package xray

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"math/big"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"crypto/ecdh"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

func TestClientConfigComesFromTheLinkOnly(t *testing.T) {
	a := link.Inbound{
		ID: 1, Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: link.TCP, Security: link.Reality,
		SNI: "www.samsung.com", PublicKey: "PUB_A", ShortID: "aa",
		Fingerprint: "chrome",
	}
	raw, err := link.Build(a, link.Client{UUID: "11111111-1111-1111-1111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ClientConfig(raw, "127.0.0.1", 18081)
	if err != nil {
		t.Fatal(err)
	}
	text := string(cfg)
	for _, want := range []string{"PUB_A", "www.samsung.com", "11111111-1111-1111-1111-111111111111", "127.0.0.1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("client config missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "PUB_B") || strings.Contains(text, "PRIV") {
		t.Fatalf("client config has a foreign or private key\n%s", text)
	}
}

func TestCanaryPassesTraffic(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("set XRAY_BIN to run the live canary")
	}
	port := freeTCP(t)
	ep := Endpoint{
		Inbound: link.Inbound{
			ID: 1, Remark: "canary", Protocol: "vless", Port: port, Host: "203.0.113.10",
			Transport: link.TCP, Security: link.None,
		},
		Clients: []link.Client{{UUID: "11111111-1111-1111-1111-111111111111", Email: "c@hami"}},
		Listen:  "127.0.0.1",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rep, err := (Canary{Bin: bin, Endpoint: ep}).Run(ctx)
	if err != nil {
		t.Fatalf("canary: %v\n%s", err, rep.Detail)
	}
	if !rep.OK || !strings.HasPrefix(rep.Link, "vless://") {
		t.Fatalf("report: %+v", rep)
	}
}

func TestCanaryRealityPassesAndRejectsForeignKey(t *testing.T) {
	bin := os.Getenv("XRAY_BIN")
	if bin == "" {
		t.Skip("set XRAY_BIN to run the live canary")
	}
	pub, priv := realityKeys(t)
	dest := tlsDest(t)
	port := freeTCP(t)
	ep := Endpoint{
		Inbound: link.Inbound{
			ID: 7, Remark: "reality", Protocol: "vless", Port: port, Host: "203.0.113.10",
			Transport: link.TCP, Security: link.Reality,
			SNI: "canary.local", PublicKey: pub, ShortID: "abcd1234", Fingerprint: "chrome",
		},
		Clients:    []link.Client{{UUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", Email: "r@hami"}},
		Listen:     "127.0.0.1",
		PrivateKey: priv,
		Dest:       dest,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rep, err := (Canary{Bin: bin, Endpoint: ep}).Run(ctx)
	if err != nil {
		t.Fatalf("reality canary: %v\n%s", err, rep.Detail)
	}
	if strings.Contains(rep.Link, priv) || !strings.Contains(rep.Link, pub) {
		t.Fatalf("link: %s", rep.Link)
	}

	// A link that borrowed another inbound's public key must not be repaired
	// from the server side. The client config is built only from the link.
	foreign := strings.Replace(rep.Link, pub, "PUB_FOREIGN_NOT_THIS_INBOUND", 1)
	cfg, err := ClientConfig(foreign, "127.0.0.1", 18082)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), pub) || !strings.Contains(string(cfg), "PUB_FOREIGN_NOT_THIS_INBOUND") {
		t.Fatal("tampered link was repaired from the server side")
	}
}

func freeTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func realityKeys(t *testing.T) (public, private string) {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(key.Bytes())
}

func tlsDest(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"canary.local"},
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
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				buf := make([]byte, 1)
				_, _ = c.Read(buf)
			}(c)
		}
	}()
	return ln.Addr().String()
}
