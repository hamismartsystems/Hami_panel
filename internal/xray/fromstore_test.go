package xray

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func openMemStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestStoreConfigOmitsDisabledInboundSecrets(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/panel.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	live := store.Inbound{
		Remark: "live", Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: "tcp", Security: "reality", SNI: "www.samsung.com",
		PublicKey: "PUB_LIVE", ShortID: "aa", Fingerprint: "chrome", Flow: "xtls-rprx-vision",
	}
	if err := st.CreateInbound(&live); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboundSecret(store.InboundSecret{
		InboundID: live.ID, PrivateKey: "PRIV_LIVE", Dest: "www.samsung.com:443",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateClient(&store.Client{InboundID: live.ID, UUID: "uuid-live", Email: "live@hami"}); err != nil {
		t.Fatal(err)
	}

	dead := store.Inbound{
		Remark: "dead", Protocol: "vless", Port: 444, Host: "203.0.113.99",
		Transport: "tcp", Security: "reality", SNI: "www.microsoft.com",
		PublicKey: "PUB_DEAD", ShortID: "bb",
	}
	if err := st.CreateInbound(&dead); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboundSecret(store.InboundSecret{
		InboundID: dead.ID, PrivateKey: "PRIV_DEAD", Dest: "www.microsoft.com:443",
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboundEnabled(dead.ID, false); err != nil {
		t.Fatal(err)
	}

	eps, err := EndpointsFromStore(st)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || eps[0].Inbound.ID != live.ID {
		t.Fatalf("endpoints: %+v", eps)
	}
	raw, err := Build(eps)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(raw)
	if strings.Contains(blob, "PRIV_DEAD") || strings.Contains(blob, "PUB_DEAD") || strings.Contains(blob, "203.0.113.99") {
		t.Fatalf("disabled inbound leaked into the config:\n%s", blob)
	}
	if !strings.Contains(blob, "PRIV_LIVE") {
		t.Fatal("live private key missing from config")
	}
	links, err := Links(eps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(links[0], "PRIV_LIVE") || strings.Contains(links[0], "PRIV_DEAD") {
		t.Fatal("private key leaked into the link")
	}
	if !strings.Contains(links[0], "PUB_LIVE") || strings.Contains(links[0], "PUB_DEAD") {
		t.Fatalf("link: %s", links[0])
	}
}

// A real inbound can carry one customer on vision and the rest on none.
// The generated config has to say so per client, or the odd one out
// simply stops connecting the moment the panel takes over.
func TestConfigKeepsEachClientsOwnFlow(t *testing.T) {
	st := openMemStore(t)
	in := &store.Inbound{
		Remark: "Reality-443", Protocol: "vless", Port: 443, Host: "198.51.100.10",
		Transport: "tcp", Security: "reality", SNI: "www.example.com",
		PublicKey: "PUB", ShortID: "aabb", Fingerprint: "chrome",
		Enable: true, Flow: "",
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboundSecret(store.InboundSecret{
		InboundID: in.ID, PrivateKey: "PRIV", Dest: "www.example.com:443", Listen: "0.0.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ email, uuid, flow string }{
		{"vision-user", "11111111-1111-1111-1111-111111111111", "xtls-rprx-vision"},
		{"plain-user", "22222222-2222-2222-2222-222222222222", ""},
	} {
		cl := &store.Client{
			InboundID: in.ID, UUID: c.uuid, Email: c.email, Flow: c.flow,
		}
		if err := st.CreateClient(cl); err != nil {
			t.Fatal(err)
		}
	}

	eps, err := EndpointsFromStore(st)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Build(eps)
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
					Flow  string `json:"flow"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range cfg.Inbounds[0].Settings.Clients {
		got[c.Email] = c.Flow
	}
	if got["vision-user"] != "xtls-rprx-vision" {
		t.Errorf("vision-user flow in the config: %q", got["vision-user"])
	}
	if got["plain-user"] != "" {
		t.Errorf("plain-user must have no flow, got %q", got["plain-user"])
	}
}

// A customer whose term has ended must not appear in the core's config.
// Their subscription link was already withheld, but the link they saved
// months ago would keep working, because the core has no idea what an
// expiry date is. Leaving them out of the config is what ends the term.
func TestExpiredClientIsNotInTheConfig(t *testing.T) {
	st := openMemStore(t)
	reason := "A customer who stopped paying kept a working link"
	in := store.Inbound{
		Remark: "live", Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: "tcp", Security: "reality", SNI: "www.samsung.com",
		PublicKey: "PUB", ShortID: "aa", Fingerprint: "chrome",
	}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboundSecret(store.InboundSecret{
		InboundID: in.ID, PrivateKey: "PRIV", Dest: "www.samsung.com:443",
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(24 * time.Hour)
	for _, c := range []*store.Client{
		{InboundID: in.ID, UUID: "uuid-ok", Email: "paid@hami", ExpireAt: &future},
		{InboundID: in.ID, UUID: "uuid-old", Email: "lapsed@hami", ExpireAt: &past},
	} {
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
	}

	eps, err := endpointsAt(st, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 {
		t.Fatalf("endpoints = %d, want 1", len(eps))
	}
	emails := make([]string, 0, len(eps[0].Clients))
	for _, c := range eps[0].Clients {
		emails = append(emails, c.Email)
	}
	if len(emails) != 1 || emails[0] != "paid@hami" {
		t.Errorf("config clients = %v; want only the client inside its term — %s", emails, reason)
	}
	// The record must survive: renewing has to bring the same account back.
	if _, err := st.GetClientByEmail("lapsed@hami"); err != nil {
		t.Errorf("dropping a client from the config removed its record: %v", err)
	}
}

// A client that has used up its quota is out of the config too, and its
// bytes stay counted.
func TestClientOverQuotaIsNotInTheConfig(t *testing.T) {
	st := openMemStore(t)
	in := store.Inbound{
		Remark: "live", Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: "tcp", Security: "none",
	}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatal(err)
	}
	full := &store.Client{InboundID: in.ID, UUID: "uuid-full", Email: "full@hami",
		TotalBytes: 1000, UpBytes: 600, DownBytes: 500}
	if err := st.CreateClient(full); err != nil {
		t.Fatal(err)
	}

	eps, err := endpointsAt(st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || len(eps[0].Clients) != 0 {
		t.Fatalf("a client past its quota is still in the config: %+v", eps)
	}
}

// A disabled client is still left out, and a client with no expiry or
// quota is still served — the new gate must not quietly drop anyone else.
func TestActiveAndUnlimitedClientsStayInTheConfig(t *testing.T) {
	st := openMemStore(t)
	in := store.Inbound{
		Remark: "live", Protocol: "vless", Port: 443, Host: "203.0.113.10",
		Transport: "tcp", Security: "none",
	}
	if err := st.CreateInbound(&in); err != nil {
		t.Fatal(err)
	}
	unlimited := &store.Client{InboundID: in.ID, UUID: "uuid-u", Email: "u@hami"}
	if err := st.CreateClient(unlimited); err != nil {
		t.Fatal(err)
	}
	off := &store.Client{InboundID: in.ID, UUID: "uuid-off", Email: "off@hami"}
	if err := st.CreateClient(off); err != nil {
		t.Fatal(err)
	}
	if err := st.SetClientEnabled(off.ID, false); err != nil {
		t.Fatal(err)
	}

	eps, err := endpointsAt(st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || len(eps[0].Clients) != 1 || eps[0].Clients[0].Email != "u@hami" {
		t.Fatalf("an unlimited client was dropped or a disabled one kept: %+v", eps)
	}
}
