package link

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

/* The tests below encode the failure modes we have actually seen in other
   panels. If someone "optimises" the builder by looking up TLS/Reality
   parameters globally again, these tests must fail. */

func TestVLESSRealityTCP(t *testing.T) {
	in := Inbound{
		ID: 1, Remark: "Reality-443", Protocol: "vless", Port: 443,
		Host: "198.51.100.10", Transport: TCP, Security: Reality,
		SNI: "www.samsung.com", PublicKey: "PUBKEY", ShortID: "abc123",
		Fingerprint: "chrome", Flow: "xtls-rprx-vision",
	}
	got, err := Build(in, Client{UUID: "11111111-2222-3333-4444-555555555555"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("link does not parse: %v (%s)", err, got)
	}
	if u.Scheme != "vless" || u.User.String() != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("bad scheme/user: %s", got)
	}
	if u.Host != "198.51.100.10:443" {
		t.Fatalf("bad host/port: %s", u.Host)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"security": "reality", "type": "tcp", "sni": "www.samsung.com",
		"pbk": "PUBKEY", "sid": "abc123", "fp": "chrome", "flow": "xtls-rprx-vision",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

// THE regression test: an XHTTP inbound must never receive Reality parameters
// from another inbound (3x-ui #4987 / #6424).
func TestXHTTPInboundHasNoRealityLeak(t *testing.T) {
	realityInbound := Inbound{
		ID: 7, Protocol: "vless", Port: 8443, Host: "198.51.100.10",
		Transport: TCP, Security: Reality, SNI: "www.samsung.com",
		PublicKey: "MASTER-PUBKEY", ShortID: "deadbeef",
	}
	xhttpInbound := Inbound{
		ID: 19, Protocol: "vless", Port: 6110, Host: "198.51.100.10",
		Transport: XHTTP, Security: TLS, SNI: "cdn.example.com",
		Path: "/abcdef", XHTTPMode: "auto",
	}

	// build both through the same API, in the "wrong" order on purpose
	_, err := Build(realityInbound, Client{UUID: "u1"})
	if err != nil {
		t.Fatalf("reality link failed: %v", err)
	}
	got, err := Build(xhttpInbound, Client{UUID: "u2"})
	if err != nil {
		t.Fatalf("xhttp link failed: %v", err)
	}

	u, _ := url.Parse(got)
	q := u.Query()
	if u.Host != "198.51.100.10:6110" {
		t.Errorf("port leaked from another inbound: %s", u.Host)
	}
	for _, k := range []string{"pbk", "sid", "spx"} {
		if q.Get(k) != "" {
			t.Errorf("reality parameter %q leaked into the xhttp link (%s)", k, q.Get(k))
		}
	}
	if q.Get("security") != "tls" || q.Get("sni") != "cdn.example.com" {
		t.Errorf("xhttp inbound lost its own tls settings: %s", got)
	}
	if q.Get("path") != "/abcdef" || q.Get("type") != "xhttp" {
		t.Errorf("xhttp transport settings wrong: %s", got)
	}
	if q.Get("flow") != "" {
		t.Errorf("flow must not be set without a reality/tls vision setup: %s", got)
	}
}

func TestValidationCatchesHalfConfiguredInbounds(t *testing.T) {
	cases := []struct {
		name string
		in   Inbound
	}{
		{"no host", Inbound{Protocol: "vless", Port: 443, Security: None}},
		{"bad port", Inbound{Host: "1.2.3.4", Protocol: "vless", Port: 0}},
		{"reality without key", Inbound{Host: "1.2.3.4", Port: 443, Protocol: "vless", Security: Reality, SNI: "x"}},
		{"reality without sni", Inbound{Host: "1.2.3.4", Port: 443, Protocol: "vless", Security: Reality, PublicKey: "k"}},
		{"ws without path", Inbound{Host: "1.2.3.4", Port: 443, Protocol: "vless", Transport: WebSocket, Security: TLS, SNI: "x"}},
		{"unknown protocol", Inbound{Host: "1.2.3.4", Port: 443, Protocol: "magic"}},
	}
	for _, c := range cases {
		if _, err := Build(c.in, Client{UUID: "u"}); err == nil {
			t.Errorf("%s: expected an error, got a link", c.name)
		}
	}
}

func TestOtherProtocols(t *testing.T) {
	base := Inbound{ID: 3, Remark: "node", Host: "vpn.example.com", Port: 443,
		Transport: TCP, Security: TLS, SNI: "vpn.example.com"}

	if l, err := Build(withProto(base, "trojan"), Client{Password: "secret"}); err != nil ||
		!strings.HasPrefix(l, "trojan://secret@vpn.example.com:443") {
		t.Errorf("trojan link wrong: %s (%v)", l, err)
	}
	if l, err := Build(withProto(base, "shadowsocks"), Client{Method: "chacha20-ietf-poly1305", SSPassword: "pw"}); err != nil ||
		!strings.HasPrefix(l, "ss://") {
		t.Errorf("ss link wrong: %s (%v)", l, err)
	}
	vm, err := Build(withProto(base, "vmess"), Client{UUID: "uuid-1"})
	if err != nil || !strings.HasPrefix(vm, "vmess://") {
		t.Fatalf("vmess link wrong: %s (%v)", vm, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(vm, "vmess://"))
	if err != nil {
		t.Fatalf("vmess payload is not base64: %v", err)
	}
	if !strings.Contains(string(raw), `"id":"uuid-1"`) {
		t.Errorf("vmess payload missing uuid: %s", raw)
	}
}

func TestSubscriptionRoundTrip(t *testing.T) {
	in := Inbound{ID: 1, Remark: "a", Protocol: "vless", Host: "1.2.3.4", Port: 443,
		Transport: TCP, Security: Reality, SNI: "s", PublicKey: "p"}
	l1, _ := Build(in, Client{UUID: "u1"})
	in2 := in
	in2.ID, in2.Remark = 2, "b"
	l2, _ := Build(in2, Client{UUID: "u2"})

	body := Subscription([]string{l1, l2})
	dec, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		t.Fatalf("subscription is not base64: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(dec)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 links, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "u1") || !strings.Contains(lines[1], "u2") {
		t.Errorf("subscription mixed up the clients: %s", dec)
	}
}

// helper: copy an inbound and override the protocol
func withProto(b Inbound, proto string) Inbound {
	b.Protocol = proto
	return b
}

// The label is for the customer, not the operator: which service, how
// much, and whether it expires. It must never print the buyer's chat id
// back at them, which is what the internal account name would do.
func TestDisplayNameReadsLikeAProduct(t *testing.T) {
	prev := DefaultBrand
	DefaultBrand = "Scorpion"
	defer func() { DefaultBrand = prev }()

	in := Inbound{Remark: "Reality-443"}
	for _, tc := range []struct {
		name  string
		c     Client
		brand string
		want  string
	}{
		{"monthly ten gigs",
			Client{Email: "tg223351591-3", QuotaBytes: 10 << 30, Timed: true}, "", "Scorpion-10GB-ماهانه"},
		{"untimed fifty gigs",
			Client{Email: "HeidarGh", QuotaBytes: 50 << 30}, "", "Scorpion-50GB-بدون‌انقضا"},
		{"no quota at all",
			Client{Email: "x", Timed: true}, "", "Scorpion-نامحدود-ماهانه"},
		{"half a gigabyte",
			Client{Email: "x", QuotaBytes: 1536 << 20}, "", "Scorpion-1.5GB-بدون‌انقضا"},
		{"per-inbound brand wins",
			Client{Email: "x", QuotaBytes: 10 << 30, Timed: true}, "HAMI", "HAMI-10GB-ماهانه"},
	} {
		i := in
		i.Brand = tc.brand
		if got := DisplayName(i, tc.c); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDisplayNameNeverLeaksTheAccountName(t *testing.T) {
	prev := DefaultBrand
	DefaultBrand = "Scorpion"
	defer func() { DefaultBrand = prev }()
	got := DisplayName(Inbound{Remark: "Reality-443"},
		Client{Email: "tg223351591-3", QuotaBytes: 10 << 30, Timed: true})
	if strings.Contains(got, "223351591") || strings.Contains(got, "tg") {
		t.Errorf("the label %q contains the buyer's internal account name", got)
	}
}
