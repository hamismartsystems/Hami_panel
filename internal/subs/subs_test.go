package subs

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func testEntry() Entry {
	exp := time.Now().Add(30 * 24 * time.Hour)
	return Entry{
		Inbound: store.Inbound{ID: 3, Remark: "Reality-443", Protocol: "vless", Port: 443,
			Host: "cdn.example.com", Transport: "tcp", Security: "reality",
			SNI: "www.microsoft.com", PublicKey: "PK", ShortID: "ab12",
			Fingerprint: "chrome", Flow: "xtls-rprx-vision"},
		Client: store.Client{ID: 7, InboundID: 3, UUID: "11111111-2222-3333-4444-555555555555",
			Email: "alice@x.y", Enable: true, TotalBytes: 50 << 30, UpBytes: 1 << 20,
			DownBytes: 2 << 20, ExpireAt: &exp, SubToken: "TOKEN22xxxxxxxxxxxxxxx"},
	}
}

func TestActiveNowGates(t *testing.T) {
	e := testEntry()
	now := time.Now()
	if !ActiveNow(e.Client, now) {
		t.Fatal("healthy client must be active")
	}
	dis := e.Client
	dis.Enable = false
	if ActiveNow(dis, now) || WhyInactive(dis, now) != "disabled" {
		t.Fatal("disabled gate")
	}
	past := now.Add(-time.Hour)
	exp := e.Client
	exp.ExpireAt = &past
	if ActiveNow(exp, now) || WhyInactive(exp, now) != "expired" {
		t.Fatal("expiry gate")
	}
	full := e.Client
	full.UpBytes = 49 << 30
	full.DownBytes = 1 << 30
	if ActiveNow(full, now) || WhyInactive(full, now) != "quota exhausted" {
		t.Fatal("quota gate")
	}
	// zero quota = unlimited
	unl := e.Client
	unl.TotalBytes = 0
	if !ActiveNow(unl, now) {
		t.Fatal("unlimited quota must stay active")
	}
}

func TestTokenFormat(t *testing.T) {
	t1, t2 := NewToken(), NewToken()
	if t1 == t2 {
		t.Fatal("tokens must not repeat")
	}
	if len(t1) != 22 {
		t.Fatalf("token length %d, want 22", len(t1))
	}
	if strings.ContainsAny(t1, "+/=") {
		t.Fatalf("token must be url-safe: %q", t1)
	}
}

func TestUserInfoHeader(t *testing.T) {
	e := testEntry()
	h := UserInfoHeader(e.Client)
	for _, part := range []string{"upload=1048576", "download=2097152", "total=53687091200", "expire="} {
		if !strings.Contains(h, part) {
			t.Fatalf("missing %q in %q", part, h)
		}
	}
	// no expiry → no expire field
	c := e.Client
	c.ExpireAt = nil
	if strings.Contains(UserInfoHeader(c), "expire") {
		t.Fatal("expire must be omitted when unset")
	}
}

func TestFormatForDetection(t *testing.T) {
	cases := []struct {
		ua, explicit string
		want         Format
	}{
		{"ClashForWindows/0.20", "", FormatClash},
		{"mihomo/v1.18", "", FormatClash},
		{"Stash/2.4", "", FormatClash},
		{"sing-box/1.9", "", FormatSingbox},
		{"Shadowrocket/2.2", "", FormatShadowrocket},
		{"", "clash", FormatClash},
		{"ClashForWindows", "singbox", FormatSingbox}, // explicit wins over UA
		{"unknown-agent", "", FormatV2ray},
	}
	for _, c := range cases {
		if got := FormatFor(c.ua, c.explicit); got != c.want {
			t.Errorf("FormatFor(%q,%q)=%s want %s", c.ua, c.explicit, got, c.want)
		}
	}
}

func TestRenderV2rayBase64(t *testing.T) {
	out, err := Render([]Entry{testEntry()}, FormatV2ray)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(out))
	if err != nil {
		t.Fatalf("must be base64: %v", err)
	}
	s := string(decoded)
	for _, want := range []string{"vless://11111111-2222", "security=reality", "pbk=PK", "sid=ab12", "flow=xtls-rprx-vision"} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in %q", want, s)
		}
	}
	// empty set → empty but valid
	out, err = Render(nil, FormatV2ray)
	if err != nil || string(out) != "" {
		t.Fatalf("empty render: %q err=%v", out, err)
	}
}

func TestRenderClashWellFormed(t *testing.T) {
	out, err := Render([]Entry{testEntry(), testEntry()}, FormatClash)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"proxies:", "proxy-groups:", "rules:", "type: vless",
		"reality-opts:", "public-key: \"PK\"", "short-id: \"ab12\"", "client-fingerprint: \"chrome\"",
		"server: \"cdn.example.com\"", "port: 443", "flow: \"xtls-rprx-vision\"", "MATCH,PROXY"} {
		if !strings.Contains(s, want) {
			t.Fatalf("clash missing %q:\n%s", want, s)
		}
	}
	// two identical entries → two proxy nodes, each named; the group list
	// references both
	if strings.Count(s, "  - name: \"Reality-443\"") != 2 {
		t.Fatal("each proxy appears once as a node")
	}
	if strings.Count(s, "      - \"Reality-443\"") != 2 {
		t.Fatal("group list references every proxy")
	}
	// no stray unquoted yaml-breakers
	if strings.Contains(s, "\t") {
		t.Fatal("tabs are illegal in yaml")
	}
}

func TestRenderShadowsocks(t *testing.T) {
	e := testEntry()
	e.Inbound.Security = "none"
	e.Inbound.Protocol = "shadowsocks"
	e.Inbound.Flow = ""
	e.Client.SSPassword = "ss-pw"
	e.Client.Method = "2022-blake3-aes-128-gcm"
	e.Client.UUID = ""
	out, err := Render([]Entry{e}, FormatClash)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"type: ss", "password: \"ss-pw\"", "cipher: \"2022-blake3-aes-128-gcm\""} {
		if !strings.Contains(s, want) {
			t.Fatalf("clash ss missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "tls: true") {
		t.Fatal("no tls on none security")
	}
	so, err := Render([]Entry{e}, FormatSingbox)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct{ Outbounds []map[string]interface{} }
	if err := json.Unmarshal(so, &doc); err != nil {
		t.Fatalf("singbox must be valid json: %v", err)
	}
	if doc.Outbounds[0]["type"] != "shadowsocks" || doc.Outbounds[0]["method"] != "2022-blake3-aes-128-gcm" {
		t.Fatalf("singbox ss wrong: %v", doc.Outbounds[0])
	}
}

func TestRenderSingboxReality(t *testing.T) {
	out, err := Render([]Entry{testEntry(), testEntry()}, FormatSingbox)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(doc.Outbounds) != 3 { // 2 proxies + selector
		t.Fatalf("outbounds=%d want 3", len(doc.Outbounds))
	}
	first := doc.Outbounds[0]
	if first["type"] != "vless" || first["uuid"] == nil {
		t.Fatalf("vless node wrong: %v", first)
	}
	tls := first["tls"].(map[string]interface{})
	reality := tls["reality"].(map[string]interface{})
	if reality["public_key"] != "PK" || reality["short_id"] != "ab12" {
		t.Fatalf("reality wrong: %v", reality)
	}
	sel := doc.Outbounds[2]
	if sel["type"] != "selector" || sel["default"] != "Reality-443" {
		t.Fatalf("selector wrong: %v", sel)
	}
}

func TestSingboxSkipsXhttp(t *testing.T) {
	e := testEntry()
	e.Inbound.Transport = "xhttp"
	e.Inbound.Path = "/xp"
	out, err := Render([]Entry{e, testEntry()}, FormatSingbox)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Outbounds []map[string]interface{} `json:"outbounds"`
	}
	json.Unmarshal(out, &doc)
	if len(doc.Outbounds) != 2 { // xhttp skipped, 1 proxy + selector
		t.Fatalf("xhttp must be skipped for singbox, got %d outbounds", len(doc.Outbounds))
	}
}

func TestEntriesNodeHealthGating(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	node := &store.Node{Name: "nl-1", Address: "10.0.0.9:9100"}
	if err := st.CreateNode(node); err != nil {
		t.Fatal(err)
	}
	in := &store.Inbound{NodeID: node.ID, Remark: "nl-in", Protocol: "vless", Port: 443,
		Host: "nl.x", Transport: "tcp", Security: "reality", SNI: "cdn.x", PublicKey: "pk"}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	c := &store.Client{InboundID: in.ID, UUID: "u-1", Enable: true}
	if err := st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	// never probed → "unknown" must still be served
	if ents, _ := Entries(st, *c, now); len(ents) != 1 {
		t.Fatalf("unknown node must be served: %v", ents)
	}
	// healthy → served
	_ = st.MarkNodeStatus(node.ID, store.NodeHealthy, now)
	if ents, _ := Entries(st, *c, now); len(ents) != 1 {
		t.Fatalf("healthy node must be served: %v", ents)
	}
	// unstable → still served (degrade≠dead)
	_ = st.MarkNodeStatus(node.ID, store.NodeUnstable, now)
	if ents, _ := Entries(st, *c, now); len(ents) != 1 {
		t.Fatalf("unstable node must stay in the sub: %v", ents)
	}
	// down → dropped
	_ = st.MarkNodeStatus(node.ID, store.NodeDown, now)
	if ents, _ := Entries(st, *c, now); len(ents) != 0 {
		t.Fatalf("down node must be dropped from the sub: %v", ents)
	}
	// back up → served again (self-healing sub)
	_ = st.MarkNodeStatus(node.ID, store.NodeHealthy, now)
	if ents, _ := Entries(st, *c, now); len(ents) != 1 {
		t.Fatalf("recovered node must return to the sub: %v", ents)
	}
	// local inbounds (node 0) are never node-gated
	local := &store.Inbound{Remark: "local", Protocol: "vless", Port: 8443,
		Host: "h.x", Transport: "tcp", Security: "reality", SNI: "s.x", PublicKey: "pk"}
	if err := st.CreateInbound(local); err != nil {
		t.Fatal(err)
	}
	lc := &store.Client{InboundID: local.ID, UUID: "u-2", Enable: true}
	_ = st.CreateClient(lc)
	if ents, _ := Entries(st, *lc, now); len(ents) != 1 {
		t.Fatalf("local inbound must bypass node gating: %v", ents)
	}
}

func TestEntriesRespectGates(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := &store.Inbound{Remark: "r", Protocol: "vless", Port: 443, Host: "h.x",
		Transport: "tcp", Security: "reality", SNI: "s.x", PublicKey: "pk"}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	c := &store.Client{InboundID: in.ID, UUID: "u-1", Email: "a@b", Enable: true, TotalBytes: 10}
	if err := st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ents, err := Entries(st, *c, now)
	if err != nil || len(ents) != 1 {
		t.Fatalf("want 1 entry: %v %v", ents, err)
	}
	// drain quota → zero entries, no error, no links
	if err := st.AddTraffic(c.ID, 5, 5); err != nil {
		t.Fatal(err)
	}
	c2, _ := st.GetClient(c.ID)
	ents, err = Entries(st, *c2, now)
	if err != nil || len(ents) != 0 {
		t.Fatalf("drained client must yield no links: %v %v", ents, err)
	}
	// disabled inbound → zero entries
	_ = st.UpdateClient(c2) // keep
	if err := st.SetInboundEnabled(in.ID, false); err != nil {
		t.Fatal(err)
	}
	free := &store.Client{InboundID: in.ID, UUID: "u-2", Enable: true}
	_ = st.CreateClient(free)
	ents, _ = Entries(st, *free, now)
	if len(ents) != 0 {
		t.Fatalf("disabled inbound must yield no entries: %v", ents)
	}
}
