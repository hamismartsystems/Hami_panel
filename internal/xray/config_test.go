package xray

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

func reality(id int64, remark, sni, pub, priv, sid, dest, uuid string) Endpoint {
	return Endpoint{
		Inbound: link.Inbound{
			ID: id, Remark: remark, Protocol: "vless", Port: 443, Host: "203.0.113.10",
			Transport: link.TCP, Security: link.Reality,
			SNI: sni, PublicKey: pub, ShortID: sid, Fingerprint: "chrome",
			Flow: "xtls-rprx-vision",
		},
		Clients:    []link.Client{{UUID: uuid, Email: remark + "@hami"}},
		PrivateKey: priv,
		Dest:       dest,
	}
}

func TestRealityConfigMatchesLinkAndDoesNotLeak(t *testing.T) {
	a := reality(1, "samsung", "www.samsung.com", "PUB_A_ONLY", "PRIV_A_ONLY", "sidA", "www.samsung.com:443", "uuid-a")
	b := reality(2, "microsoft", "www.microsoft.com", "PUB_B_ONLY", "PRIV_B_ONLY", "sidB", "www.microsoft.com:443", "uuid-b")
	b.Inbound.Port = 8443
	b.Inbound.Host = "203.0.113.20"

	raw, err := Build([]Endpoint{a, b})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []struct {
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
			Settings struct {
				Clients []struct {
					ID   string `json:"id"`
					Flow string `json:"flow"`
				} `json:"clients"`
			} `json:"settings"`
			Stream struct {
				Network  string `json:"network"`
				Security string `json:"security"`
				Reality  struct {
					Dest        string   `json:"dest"`
					ServerNames []string `json:"serverNames"`
					PrivateKey  string   `json:"privateKey"`
					ShortIDs    []string `json:"shortIds"`
				} `json:"realitySettings"`
				HasTLS   json.RawMessage `json:"tlsSettings"`
				HasXHTTP json.RawMessage `json:"xhttpSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// the statistics endpoint rides along as one extra inbound
	if len(doc.Inbounds) != 3 {
		t.Fatalf("inbounds: %d", len(doc.Inbounds))
	}
	got := doc.Inbounds[0]
	if got.Port != 443 || got.Protocol != "vless" || got.Settings.Clients[0].ID != "uuid-a" {
		t.Fatalf("inbound A identity: %+v", got)
	}
	if got.Stream.Reality.PrivateKey != "PRIV_A_ONLY" || got.Stream.Reality.Dest != "www.samsung.com:443" {
		t.Fatalf("inbound A reality: %+v", got.Stream.Reality)
	}
	if got.Stream.Reality.ServerNames[0] != "www.samsung.com" || got.Stream.Reality.ShortIDs[0] != "sidA" {
		t.Fatalf("inbound A names: %+v", got.Stream.Reality)
	}
	blob := string(raw)
	// A's object must not be how we check isolation of the whole file — B's
	// secrets are allowed in B's inbound. Isolation is: A's stream settings
	// do not contain B's secrets.
	aBlob, _ := json.Marshal(doc.Inbounds[0])
	for _, foreign := range []string{"PRIV_B_ONLY", "PUB_B_ONLY", "sidB", "www.microsoft.com", "uuid-b", "203.0.113.20"} {
		if strings.Contains(string(aBlob), foreign) {
			t.Errorf("inbound A config contains inbound B value %q", foreign)
		}
	}
	if strings.Contains(blob, "PUB_A_ONLY") || strings.Contains(blob, "PUB_B_ONLY") {
		t.Error("public key must not be written into the server config")
	}

	links, err := Links([]Endpoint{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 {
		t.Fatalf("links: %d", len(links))
	}
	if strings.Contains(links[0], "PRIV_A_ONLY") || strings.Contains(links[0], "PRIV_B_ONLY") {
		t.Error("private key leaked into the link")
	}
	for _, want := range []string{"uuid-a", "www.samsung.com", "PUB_A_ONLY", "sidA", "203.0.113.10"} {
		if !strings.Contains(links[0], want) {
			t.Errorf("link A missing %q: %s", want, links[0])
		}
	}
	for _, foreign := range []string{"PUB_B_ONLY", "sidB", "www.microsoft.com", "uuid-b", "203.0.113.20"} {
		if strings.Contains(links[0], foreign) {
			t.Errorf("link A contains inbound B value %q", foreign)
		}
	}
}

func TestXHTTPDoesNotInheritReality(t *testing.T) {
	rev := reality(1, "rev", "www.samsung.com", "PUB_R", "PRIV_R", "sidR", "www.samsung.com:443", "uuid-r")
	xhttp := Endpoint{
		Inbound: link.Inbound{
			ID: 9, Remark: "xhttp", Protocol: "vless", Port: 2080, Host: "203.0.113.10",
			Transport: link.XHTTP, Security: link.None, Path: "/hami-x", XHTTPMode: "stream-one",
		},
		Clients: []link.Client{{UUID: "uuid-x", Email: "x@hami"}},
	}
	raw, err := Build([]Endpoint{rev, xhttp})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []struct {
			Port   int `json:"port"`
			Stream struct {
				Network  string          `json:"network"`
				Security string          `json:"security"`
				Reality  json.RawMessage `json:"realitySettings"`
				XHTTP    struct {
					Path string `json:"path"`
					Mode string `json:"mode"`
				} `json:"xhttpSettings"`
			} `json:"streamSettings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var xh *struct {
		Port   int `json:"port"`
		Stream struct {
			Network  string          `json:"network"`
			Security string          `json:"security"`
			Reality  json.RawMessage `json:"realitySettings"`
			XHTTP    struct {
				Path string `json:"path"`
				Mode string `json:"mode"`
			} `json:"xhttpSettings"`
		} `json:"streamSettings"`
	}
	for i := range doc.Inbounds {
		if doc.Inbounds[i].Port == 2080 {
			xh = &doc.Inbounds[i]
		}
	}
	if xh == nil {
		t.Fatal("xhttp inbound missing")
	}
	if xh.Stream.Network != "xhttp" || xh.Stream.Security != "none" {
		t.Fatalf("xhttp stream: %+v", xh.Stream)
	}
	if len(xh.Stream.Reality) > 0 && string(xh.Stream.Reality) != "null" {
		t.Fatalf("xhttp inherited reality settings: %s", xh.Stream.Reality)
	}
	if xh.Stream.XHTTP.Path != "/hami-x" || xh.Stream.XHTTP.Mode != "stream-one" {
		t.Fatalf("xhttp path/mode: %+v", xh.Stream.XHTTP)
	}
	blob, _ := json.Marshal(xh)
	if strings.Contains(string(blob), "PRIV_R") || strings.Contains(string(blob), "PUB_R") {
		t.Error("xhttp inbound contains the reality inbound's keys")
	}
}

func TestRejectsHalfConfiguredReality(t *testing.T) {
	ep := reality(1, "a", "www.samsung.com", "PUB", "", "sid", "www.samsung.com:443", "uuid")
	if _, err := Build([]Endpoint{ep}); err == nil {
		t.Fatal("empty private key must fail")
	}
	ep.PrivateKey = "PRIV"
	ep.Dest = ""
	if _, err := Build([]Endpoint{ep}); err == nil {
		t.Fatal("empty dest must fail")
	}
	ep.Dest = "www.samsung.com:443"
	ep.ShortIDs = []string{"other"}
	if _, err := Build([]Endpoint{ep}); err == nil {
		t.Fatal("link short id missing from server list must fail")
	}
}

func TestTLSWithoutCertFails(t *testing.T) {
	ep := Endpoint{
		Inbound: link.Inbound{
			ID: 3, Protocol: "trojan", Port: 443, Host: "example.com",
			Transport: link.TCP, Security: link.TLS, SNI: "example.com",
		},
		Clients: []link.Client{{Password: "secret"}},
	}
	if _, err := Build([]Endpoint{ep}); err == nil {
		t.Fatal("tls without certificate must fail")
	}
	ep.CertFile = "/etc/hami/a.crt"
	ep.KeyFile = "/etc/hami/a.key"
	raw, err := Build([]Endpoint{ep})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "/etc/hami/a.crt") {
		t.Fatal("cert path missing")
	}
	links, err := Links([]Endpoint{ep})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(links[0], "/etc/hami/a.key") || strings.Contains(links[0], "a.crt") {
		t.Fatal("certificate path leaked into the link")
	}
}

// Without a statistics endpoint the panel cannot count a byte of a
// customer's traffic or tell whether anyone is connected, so quotas
// never deplete. It must be present, and it must be on loopback.
func TestConfigExposesStatsOnLoopbackOnly(t *testing.T) {
	raw, err := Build([]Endpoint{{
		Inbound: link.Inbound{
			ID: 1, Remark: "a", Protocol: "vless", Port: 443, Host: "198.51.100.10",
			Transport: link.TCP, Security: link.None,
		},
		Clients: []link.Client{{UUID: "11111111-1111-1111-1111-111111111111", Email: "x"}},
		Listen:  "0.0.0.0", Tag: "in-1",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		API struct {
			Tag      string   `json:"tag"`
			Services []string `json:"services"`
		} `json:"api"`
		Stats  map[string]any `json:"stats"`
		Policy struct {
			Levels map[string]struct {
				Up     bool `json:"statsUserUplink"`
				Down   bool `json:"statsUserDownlink"`
				Online bool `json:"statsUserOnline"`
			} `json:"levels"`
		} `json:"policy"`
		Routing struct {
			Rules []struct {
				InboundTag  []string `json:"inboundTag"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
		Inbounds []struct {
			Tag    string `json:"tag"`
			Listen string `json:"listen"`
			Port   int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Stats == nil {
		t.Error("no stats block: nothing would ever be counted")
	}
	lvl := doc.Policy.Levels["0"]
	if !lvl.Up || !lvl.Down {
		t.Error("per-user traffic counting is off, so quotas can never deplete")
	}
	if !lvl.Online {
		t.Error("per-user online counting is off, so everyone looks offline")
	}
	var api struct {
		listen string
		port   int
		found  bool
	}
	for _, in := range doc.Inbounds {
		if in.Tag == "api" {
			api.listen, api.port, api.found = in.Listen, in.Port, true
		}
	}
	if !api.found {
		t.Fatal("no api inbound")
	}
	if api.listen != "127.0.0.1" {
		t.Errorf("the stats endpoint listens on %q — it must never leave the machine", api.listen)
	}
	if api.port != StatsAPIPort {
		t.Errorf("api port %d, want %d", api.port, StatsAPIPort)
	}
	routed := false
	for _, r := range doc.Routing.Rules {
		for _, tag := range r.InboundTag {
			if tag == "api" && r.OutboundTag == "api" {
				routed = true
			}
		}
	}
	if !routed {
		t.Error("the api inbound is not routed to the api service, so it answers nothing")
	}
}
