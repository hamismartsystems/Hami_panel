package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func parseIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("%q is not an IP", s)
	}
	return ip
}

func request(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

// The throttle key must come from the socket, not from a header any
// visitor can set: a fresh key per request would mean no throttle at all.
func TestForwardedForIsIgnoredWithoutATrustedProxy(t *testing.T) {
	s := &Server{}
	r := request("203.0.113.7:51234", "1.2.3.4")
	if got := s.clientKey(r); got != "203.0.113.7" {
		t.Errorf("key = %q; a visitor can pick its own throttle key "+
			"by sending X-Forwarded-For", got)
	}
}

// With a named proxy in front, the last hop is the one that proxy
// appended — the visitor can only add fields to the left of it.
func TestTrustedProxyUsesTheLastHop(t *testing.T) {
	tp, err := ParseTrustedProxies("127.0.0.1/8")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.SetTrustedProxies(tp)

	r := request("127.0.0.1:51234", "198.51.100.9, 203.0.113.7")
	if got := s.clientKey(r); got != "203.0.113.7" {
		t.Errorf("key = %q, want the hop the trusted proxy appended", got)
	}

	// Several header lines are the same as one comma-separated list.
	r = request("127.0.0.1:51234", "198.51.100.9", "203.0.113.8")
	if got := s.clientKey(r); got != "203.0.113.8" {
		t.Errorf("key = %q, want the last hop across header lines", got)
	}

	// A trusted proxy with no forwarding information falls back to itself.
	r = request("127.0.0.1:51234")
	if got := s.clientKey(r); got != "127.0.0.1" {
		t.Errorf("key = %q, want the socket address", got)
	}
}

// A malformed field must not become a key of the visitor's choosing.
func TestGarbageForwardedForIsRefused(t *testing.T) {
	tp, err := ParseTrustedProxies("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.SetTrustedProxies(tp)
	r := request("10.0.0.5:1234", "203.0.113.7, not-an-ip")
	if got := s.clientKey(r); got != "10.0.0.5" {
		t.Errorf("key = %q, want the socket address when the header is junk", got)
	}
}

func TestParseTrustedProxies(t *testing.T) {
	if tp, err := ParseTrustedProxies(""); err != nil || tp != nil {
		t.Errorf("empty list = %v, %v; want no trusted proxies and no error", tp, err)
	}
	tp, err := ParseTrustedProxies(" 127.0.0.0/8 , ::1 , 10.0.0.0/8 ")
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"127.0.0.1", "127.0.0.9", "::1", "10.1.2.3"} {
		if !tp.contains(parseIP(t, ip)) {
			t.Errorf("%s should be trusted", ip)
		}
	}
	if tp.contains(parseIP(t, "203.0.113.7")) {
		t.Error("203.0.113.7 is not on the list and must not be trusted")
	}

	// A bare address is one host, not the network it happens to sit in.
	single, err := ParseTrustedProxies("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !single.contains(parseIP(t, "127.0.0.1")) || single.contains(parseIP(t, "127.0.0.9")) {
		t.Error("a bare address must trust exactly that one address")
	}
	// A bare ipv6 address gets a /128, not a /32.
	six, err := ParseTrustedProxies("::1")
	if err != nil {
		t.Fatal(err)
	}
	if !six.contains(parseIP(t, "::1")) || six.contains(parseIP(t, "2001:db8::1")) {
		t.Error("a bare ipv6 address must trust exactly that one address")
	}
	if _, err := ParseTrustedProxies("definitely-not-a-cidr"); err == nil {
		t.Error("a nonsense proxy spec was accepted")
	}
}
