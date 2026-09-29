package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

func TestHysteria2LinkAndConfig(t *testing.T) {
	in := link.Inbound{
		ID: 1, Remark: "hy2-test", Protocol: "hysteria2", Port: 8443,
		Host: "example.com", SNI: "example.com",
		ObfsType: "salamander", ObfsPassword: "obfs123",
	}
	client := link.Client{Password: "secret123", Email: "user@example.com"}

	got, err := link.Build(in, client)
	if err != nil {
		t.Fatalf("link build: %v", err)
	}
	if !strings.HasPrefix(got, "hysteria2://secret123@example.com:8443") {
		t.Fatalf("bad hy2 link: %s", got)
	}
	if !strings.Contains(got, "obfs=salamander") || !strings.Contains(got, "sni=example.com") {
		t.Fatalf("hy2 link missing params: %s", got)
	}

	ep := Endpoint{
		Inbound:  in,
		Clients:  []link.Client{client},
		CertFile: "/etc/ssl/cert.pem",
		KeyFile:  "/etc/ssl/key.pem",
	}
	cfg, err := Build([]Endpoint{ep})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(cfg, &doc); err != nil {
		t.Fatalf("json: %v", err)
	}
	inbounds, ok := doc["inbounds"].([]any)
	if !ok || len(inbounds) != 1 {
		t.Fatalf("inbounds wrong: %v", doc["inbounds"])
	}
	ib := inbounds[0].(map[string]any)
	if ib["type"] != "hysteria2" {
		t.Fatalf("type = %v", ib["type"])
	}
}

func TestTUICLinkAndConfig(t *testing.T) {
	in := link.Inbound{
		ID: 2, Remark: "tuic-test", Protocol: "tuic", Port: 443,
		Host: "example.com", SNI: "example.com",
		Alpn: "h3", CongestionControl: "bbr",
	}
	client := link.Client{UUID: "11111111-2222-3333-4444-555555555555", Password: "pw123"}

	got, err := link.Build(in, client)
	if err != nil {
		t.Fatalf("tuic link: %v", err)
	}
	if !strings.HasPrefix(got, "tuic://") {
		t.Fatalf("bad tuic link: %s", got)
	}
	if !strings.Contains(got, "11111111-2222-3333-4444-555555555555") {
		t.Fatalf("uuid missing: %s", got)
	}
	if !strings.Contains(got, "congestion_control=bbr") {
		t.Fatalf("cc missing: %s", got)
	}

	ep := Endpoint{
		Inbound:  in,
		Clients:  []link.Client{client},
		CertFile: "/etc/ssl/cert.pem",
		KeyFile:  "/etc/ssl/key.pem",
	}
	cfg, err := Build([]Endpoint{ep})
	if err != nil {
		t.Fatalf("build tuic: %v", err)
	}
	if !strings.Contains(string(cfg), "\"type\": \"tuic\"") {
		t.Fatalf("config missing tuic type: %s", cfg)
	}
}

func TestAnyTLSLinkAndConfig(t *testing.T) {
	in := link.Inbound{
		ID: 3, Remark: "anytls-test", Protocol: "anytls", Port: 443,
		Host: "example.com", SNI: "example.com",
	}
	client := link.Client{Password: "anytls-secret"}

	got, err := link.Build(in, client)
	if err != nil {
		t.Fatalf("anytls link: %v", err)
	}
	if !strings.HasPrefix(got, "anytls://") {
		t.Fatalf("bad anytls link: %s", got)
	}
	if !strings.Contains(got, "sni=example.com") {
		t.Fatalf("sni missing: %s", got)
	}

	ep := Endpoint{
		Inbound:  in,
		Clients:  []link.Client{client},
		CertFile: "/etc/ssl/cert.pem",
		KeyFile:  "/etc/ssl/key.pem",
	}
	cfg, err := Build([]Endpoint{ep})
	if err != nil {
		t.Fatalf("build anytls: %v", err)
	}
	if !strings.Contains(string(cfg), "anytls") {
		t.Fatalf("config missing anytls: %s", cfg)
	}
}

func TestAnyTLSRealityConfig(t *testing.T) {
	in := link.Inbound{
		ID: 4, Remark: "anytls-reality", Protocol: "anytls", Port: 443,
		Host: "example.com", SNI: "www.microsoft.com",
	}
	client := link.Client{Password: "secret"}
	ep := Endpoint{
		Inbound:    in,
		Clients:    []link.Client{client},
		PrivateKey: "fake-private-key",
		Dest:       "www.microsoft.com:443",
		ShortIDs:   []string{"abc123"},
	}
	cfg, err := Build([]Endpoint{ep})
	if err != nil {
		t.Fatalf("build anytls reality: %v", err)
	}
	if !strings.Contains(string(cfg), "reality") {
		t.Fatalf("reality not in config: %s", cfg)
	}
}
