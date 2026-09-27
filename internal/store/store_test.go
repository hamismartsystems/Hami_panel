package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	// each test gets its own file so they never influence each other
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrationsCreateSchema(t *testing.T) {
	s := openTest(t)
	for _, table := range []string{"inbounds", "clients", "traffic", "nodes", "events"} {
		var name string
		if err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).
			Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}
	// running the migration twice must be harmless (idempotent)
	if err := s.migrate(); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestInboundRoundTrip(t *testing.T) {
	s := openTest(t)
	in := Inbound{
		Remark: "Reality-443", Protocol: "vless", Port: 443, Host: "198.51.100.10",
		Transport: "tcp", Security: "reality", SNI: "www.samsung.com",
		PublicKey: "PUB", ShortID: "abc", Fingerprint: "chrome", Flow: "xtls-rprx-vision",
	}
	if err := s.CreateInbound(&in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.ID == 0 {
		t.Fatal("id was not assigned")
	}
	got, err := s.GetInbound(in.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Port != 443 || got.Host != "198.51.100.10" || got.PublicKey != "PUB" ||
		got.SNI != "www.samsung.com" || got.Flow != "xtls-rprx-vision" {
		t.Fatalf("fields did not survive the round trip: %+v", got)
	}
	if !got.Enable {
		t.Error("a new inbound must be enabled by default")
	}
}

func TestDefaultsAreFilled(t *testing.T) {
	s := openTest(t)
	in := Inbound{Protocol: "vless", Port: 443, Host: "1.2.3.4"} // security intentionally empty
	if err := s.CreateInbound(&in); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _ := s.GetInbound(in.ID)
	if got.Security != "none" || got.Transport != "tcp" || got.Fingerprint != "chrome" {
		t.Errorf("defaults not applied: %+v", got)
	}
}

func TestValidationOfIncompleteInbound(t *testing.T) {
	s := openTest(t)
	cases := []Inbound{
		{Protocol: "vless", Port: 443},       // no host
		{Protocol: "vless", Host: "1.2.3.4"}, // no port
		{Port: 443, Host: "1.2.3.4"},         // no protocol
	}
	for i, c := range cases {
		if err := s.CreateInbound(&c); err == nil {
			t.Errorf("case %d: expected an error", i)
		}
	}
}

// The whole point of the panel: an inbound read back from the database must
// produce exactly the link we expect — nothing borrowed from anywhere else.
func TestStoredInboundProducesExpectedLink(t *testing.T) {
	s := openTest(t)

	reality := Inbound{
		Remark: "master", Protocol: "vless", Port: 8443, Host: "198.51.100.10",
		Transport: "tcp", Security: "reality", SNI: "www.samsung.com",
		PublicKey: "MASTER-KEY", ShortID: "deadbeef",
	}
	xhttp := Inbound{
		Remark: "xhttp-node", Protocol: "vless", Port: 6110, Host: "198.51.100.10",
		Transport: "xhttp", Security: "tls", SNI: "cdn.example.com",
		Path: "/abcdef", XHTTPMode: "auto",
	}
	if err := s.CreateInbound(&reality); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateInbound(&xhttp); err != nil {
		t.Fatal(err)
	}

	// clients attached to their own inbound
	c1 := Client{InboundID: reality.ID, UUID: "uuid-master", Email: "a@x"}
	c2 := Client{InboundID: xhttp.ID, UUID: "uuid-xhttp", Email: "b@x"}
	if err := s.CreateClient(&c1); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateClient(&c2); err != nil {
		t.Fatal(err)
	}

	l2, err := buildLinkFor(s, c2.ID)
	if err != nil {
		t.Fatalf("link for xhttp client: %v", err)
	}
	if strings.Contains(l2, "MASTER-KEY") || strings.Contains(l2, "deadbeef") {
		t.Errorf("reality settings leaked into the xhttp link: %s", l2)
	}
	if !strings.Contains(l2, ":6110") || !strings.Contains(l2, "uuid-xhttp") {
		t.Errorf("xhttp link lost its own identity: %s", l2)
	}

	l1, err := buildLinkFor(s, c1.ID)
	if err != nil {
		t.Fatalf("link for reality client: %v", err)
	}
	if !strings.Contains(l1, "pbk=MASTER-KEY") || !strings.Contains(l1, ":8443") {
		t.Errorf("reality link wrong: %s", l1)
	}
}

// buildLinkFor resolves the client's OWN inbound and builds the link from it.
func buildLinkFor(s *Store, clientID int64) (string, error) {
	c, err := s.GetClient(clientID)
	if err != nil {
		return "", err
	}
	in, err := s.GetInbound(c.InboundID)
	if err != nil {
		return "", err
	}
	return link.Build(inboundToLink(*in), link.Client{
		UUID: c.UUID, Password: c.Password, Method: c.Method, SSPassword: c.SSPassword,
	})
}

func TestTrafficAccounting(t *testing.T) {
	s := openTest(t)
	in := Inbound{Protocol: "vless", Port: 443, Host: "1.2.3.4", Security: "none"}
	if err := s.CreateInbound(&in); err != nil {
		t.Fatal(err)
	}
	c := Client{InboundID: in.ID, UUID: "u1", TotalBytes: 100}
	if err := s.CreateClient(&c); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraffic(c.ID, 10, 20); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTraffic(c.ID, 5, 5); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetClient(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.UpBytes != 15 || got.DownBytes != 25 {
		t.Errorf("counters wrong: up=%d down=%d", got.UpBytes, got.DownBytes)
	}
	if got.TotalBytes != 100 {
		t.Errorf("quota must not be touched by usage: %d", got.TotalBytes)
	}
}

func TestExpiryAndAuditLog(t *testing.T) {
	s := openTest(t)
	in := Inbound{Protocol: "trojan", Port: 443, Host: "1.2.3.4", Security: "tls", SNI: "x"}
	if err := s.CreateInbound(&in); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().UTC().AddDate(0, 1, 0).Truncate(time.Second)
	c := Client{InboundID: in.ID, Password: "pw", ExpireAt: &exp}
	if err := s.CreateClient(&c); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetClient(c.ID)
	if got.ExpireAt == nil || !got.ExpireAt.Equal(exp) {
		t.Fatalf("expiry did not survive: %v", got.ExpireAt)
	}

	if err := s.AddEvent("warn", "system", "shareAddr was wrong and got fixed", "{\"from\":\"1.2.3.4\"}"); err != nil {
		t.Fatal(err)
	}
	ev, err := s.RecentEvents(10)
	if err != nil || len(ev) != 1 {
		t.Fatalf("events: %v (%d)", err, len(ev))
	}
	if ev[0].Message != "shareAddr was wrong and got fixed" || ev[0].Level != "warn" {
		t.Errorf("event content wrong: %+v", ev[0])
	}
}

func TestDeletingInboundRemovesItsClients(t *testing.T) {
	s := openTest(t)
	in := Inbound{Protocol: "vless", Port: 443, Host: "1.2.3.4"}
	if err := s.CreateInbound(&in); err != nil {
		t.Fatal(err)
	}
	c := Client{InboundID: in.ID, UUID: "u"}
	if err := s.CreateClient(&c); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInbound(in.ID); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListClientsOf(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("clients survived the deletion of their inbound: %d", len(list))
	}
}

/* helper the panel will use: store.Inbound -> link.Inbound */
func inboundToLink(i Inbound) link.Inbound {
	return link.Inbound{
		ID: i.ID, Remark: i.Remark, Protocol: i.Protocol, Port: i.Port, Host: i.Host,
		Transport: link.Transport(i.Transport), Security: link.Security(i.Security),
		SNI: i.SNI, PublicKey: i.PublicKey, ShortID: i.ShortID, SpiderX: i.SpiderX,
		Fingerprint: i.Fingerprint, Path: i.Path, XHTTPMode: i.XHTTPMode,
		HeaderType: i.HeaderType, Flow: i.Flow,
	}
}
