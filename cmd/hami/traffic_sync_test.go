package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func copyTo(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// editInbound rewrites the clients of one inbound in a copy of the config.
func editInbound(t *testing.T, cfg, tag string, edit func([]any) []any) {
	t.Helper()
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ib := range doc["inbounds"].([]any) {
		m := ib.(map[string]any)
		if m["tag"] != tag {
			continue
		}
		found = true
		settings := m["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		settings["clients"] = edit(clients)
	}
	if !found {
		t.Fatalf("no inbound %s", tag)
	}
	out, _ := json.Marshal(doc)
	if err := os.WriteFile(cfg, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) (*store.Store, string) {
	t.Helper()
	if _, err := os.Stat("/tmp/drifttest/panel.db"); err != nil {
		t.Skip("fixture /tmp/drifttest not present")
	}
	dir := t.TempDir()
	db := filepath.Join(dir, "panel.db")
	cfg := filepath.Join(dir, "xray.json")
	copyTo(t, "/tmp/drifttest/panel.db", db)
	copyTo(t, "/tmp/drifttest/xray.json", cfg)
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, cfg
}

func TestCoreDriftInSync(t *testing.T) {
	st, cfg := setup(t)
	d, why, err := coreDrift(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if d {
		t.Fatalf("expected in sync, got drift: %s", why)
	}
}

func TestCoreDriftSwitchedOffStillInCore(t *testing.T) {
	st, cfg := setup(t)
	// Simulate a missed push: a switched-off client is still in the file.
	editInbound(t, cfg, "in-1", func(cl []any) []any {
		return append(cl, map[string]any{"id": "00000000-0000-4000-8000-000000000001", "email": "Malek"})
	})
	d, why, err := coreDrift(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !d || !strings.Contains(why, "in-1|malek") {
		t.Fatalf("expected drift naming in-1|malek, got %v: %s", d, why)
	}
}

func TestCoreDriftEnabledMissingFromCore(t *testing.T) {
	st, cfg := setup(t)
	editInbound(t, cfg, "in-1", func(cl []any) []any {
		out := []any{}
		for _, c := range cl {
			if c.(map[string]any)["email"] != "test250-before" {
				out = append(out, c)
			}
		}
		return out
	})
	d, why, err := coreDrift(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !d || !strings.Contains(why, "in-1|test250-before") {
		t.Fatalf("expected drift naming missing in-1|test250-before, got %v: %s", d, why)
	}
}
