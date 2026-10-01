package xray

import (
	"encoding/json"
	"strings"
	"testing"

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
