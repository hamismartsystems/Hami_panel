package reality

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

func TestCheckDestEmpty(t *testing.T) {
	r := CheckDest("", "example.com", 0)
	if r.OK {
		t.Fatal("should fail on empty dest")
	}
}

func TestCheckDestWithLocalTLS(t *testing.T) {
	// self-signed cert for check.local
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "check.local"},
		DNSNames:              []string{"check.local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				// handshake
				if tc, ok := conn.(*tls.Conn); ok {
					_ = tc.Handshake()
				}
				conn.Close()
			}(c)
		}
	}()

	r := CheckDest(ln.Addr().String(), "check.local", 2*time.Second)
	if !r.OK {
		t.Fatalf("expected ok, got %v", r.Detail)
	}
	if r.CertCN != "check.local" {
		t.Fatalf("cn=%s", r.CertCN)
	}

	r2 := CheckDest(ln.Addr().String(), "other.local", 2*time.Second)
	if r2.OK {
		t.Fatal("should fail on wrong SNI")
	}
}

func TestGenerateKeypair(t *testing.T) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if len(priv) < 20 || len(pub) < 20 {
		t.Fatalf("keys too short %q %q", priv, pub)
	}
	sid, err := GenerateShortID(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(sid) != 8 {
		t.Fatalf("shortid len %d", len(sid))
	}
}
