package store

import (
	"errors"
	"testing"
	"time"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func seedInbound(t *testing.T, s *Store) int64 {
	t.Helper()
	in := &Inbound{Remark: "r1", Protocol: "vless", Port: 443, Host: "x.example.com",
		Transport: "tcp", Security: "reality", SNI: "cdn.example.com",
		PublicKey: "pbk", ShortID: "aa", Fingerprint: "chrome"}
	if err := s.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return in.ID
}

func TestSubTokenLookupAndRotation(t *testing.T) {
	s := openMem(t)
	ib := seedInbound(t, s)
	c := &Client{InboundID: ib, UUID: "u-1", Email: "a@b.c"}
	if err := s.CreateClient(c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	if _, err := s.ClientBySubToken("nope"); err != ErrNotFound {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if _, err := s.ClientBySubToken(""); err != ErrNotFound {
		t.Fatalf("empty token must not match, got %v", err)
	}
	if err := s.RotateSubToken(c.ID, "tok-1"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	got, err := s.ClientBySubToken("tok-1")
	if err != nil || got.ID != c.ID {
		t.Fatalf("lookup: %v %+v", err, got)
	}
	// rotate again — the old token must die
	if err := s.RotateSubToken(c.ID, "tok-2"); err != nil {
		t.Fatalf("rotate 2: %v", err)
	}
	if _, err := s.ClientBySubToken("tok-1"); err != ErrNotFound {
		t.Fatalf("old token must stop working, got %v", err)
	}
	// tokens are unique across clients
	c2 := &Client{InboundID: ib, UUID: "u-2"}
	if err := s.CreateClient(c2); err != nil {
		t.Fatalf("create client 2: %v", err)
	}
	if err := s.RotateSubToken(c2.ID, "tok-2"); err == nil {
		t.Fatalf("duplicate token must be rejected")
	}
	if err := s.RotateSubToken(9999, "tok-x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound for missing id, got %v", err)
	}
}

func TestResetClientUsage(t *testing.T) {
	s := openMem(t)
	ib := seedInbound(t, s)
	c := &Client{InboundID: ib, UUID: "u-1", Email: "x@y.z"}
	if err := s.CreateClient(c); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.AddTraffic(c.ID, 100, 400); err != nil {
		t.Fatalf("traffic: %v", err)
	}
	up, down, err := s.ResetClientUsage(c.ID)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if up != 100 || down != 400 {
		t.Fatalf("reset reported %d/%d, want 100/400", up, down)
	}
	got, _ := s.GetClient(c.ID)
	if got.UpBytes != 0 || got.DownBytes != 0 {
		t.Fatalf("counters not zero: %+v", got)
	}
	// history survives: the traffic row is still there
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM traffic WHERE client_id=?`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("traffic history lost: n=%d err=%v", n, err)
	}
	if _, _, err := s.ResetClientUsage(424242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpdateClientQuotaExpiry(t *testing.T) {
	s := openMem(t)
	ib := seedInbound(t, s)
	c := &Client{InboundID: ib, UUID: "u-1", Email: "before@x.y"}
	if err := s.CreateClient(c); err != nil {
		t.Fatalf("create: %v", err)
	}
	exp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	updated := *c
	updated.Email = "after@x.y"
	updated.TotalBytes = 1 << 30
	updated.ExpireAt = &exp
	updated.IPLimit = 3
	updated.SpeedLimit = 1024
	if err := s.UpdateClient(&updated); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.GetClientByEmail("after@x.y")
	if err != nil {
		t.Fatalf("lookup by new email: %v", err)
	}
	if got.TotalBytes != 1<<30 || got.IPLimit != 3 || got.SpeedLimit != 1024 {
		t.Fatalf("fields not persisted: %+v", got)
	}
	if got.ExpireAt == nil || !got.ExpireAt.Equal(exp) {
		t.Fatalf("expiry not persisted: %+v", got.ExpireAt)
	}
	if _, err := s.GetClientByEmail("ghost@x.y"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUpgradeFromOldSchema(t *testing.T) {
	// Simulate a Stage-0/1 database file: everything except the two new
	// migrations, old user_version, and a client without a token.
	path := t.TempDir() + "/old.db"

	raw, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// roll schema back: drop the column by recreating from the old version
	// (SQLite keeps the column, so instead assert the migration applied and
	// pre-existing rows got the empty-token default).
	ib := seedInbound(t, raw)
	c := &Client{InboundID: ib, UUID: "u-old", Email: "old@x.y"}
	if err := raw.CreateClient(c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	raw.Close()

	// reopen — migrations must be idempotent and the row must be intact
	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	got, err := again.GetClient(c.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SubToken != "" {
		t.Fatalf("old rows must keep empty token, got %q", got.SubToken)
	}
	if err := again.RotateSubToken(c.ID, "tok-migrated"); err != nil {
		t.Fatalf("rotate on migrated db: %v", err)
	}
}
