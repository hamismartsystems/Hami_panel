package apply

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seed(t *testing.T, st *store.Store, emails ...string) int64 {
	t.Helper()
	in := &store.Inbound{
		Remark: "Reality-443", Protocol: "vless", Port: 443, Host: "198.51.100.10",
		Transport: "tcp", Security: "none", Enable: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	for i, e := range emails {
		c := &store.Client{
			InboundID: in.ID, Email: e,
			UUID: "11111111-1111-1111-1111-11111111111" + string(rune('0'+i)),
		}
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
	}
	return in.ID
}

func clientsIn(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Inbounds []struct {
			Tag      string `json:"tag"`
			Settings struct {
				Clients []struct {
					Email string `json:"email"`
				} `json:"clients"`
			} `json:"settings"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, in := range doc.Inbounds {
		if in.Tag == "api" {
			continue
		}
		for _, c := range in.Settings.Clients {
			out = append(out, c.Email)
		}
	}
	return out
}

func TestApplyWritesWhatTheDatabaseHolds(t *testing.T) {
	st := newStore(t)
	seed(t, st, "ali", "reza")
	cfg := filepath.Join(t.TempDir(), "xray.json")
	a := &Applier{Store: st, ConfigPath: cfg}

	if err := a.Now(); err != nil {
		t.Fatal(err)
	}
	got := clientsIn(t, cfg)
	if len(got) != 2 {
		t.Fatalf("config holds %v", got)
	}
}

// Selling somebody a config and leaving the core unaware of them is the
// failure this whole package exists to prevent.
func TestANewCustomerReachesTheConfig(t *testing.T) {
	st := newStore(t)
	inID := seed(t, st, "ali")
	cfg := filepath.Join(t.TempDir(), "xray.json")
	a := &Applier{Store: st, ConfigPath: cfg}
	if err := a.Now(); err != nil {
		t.Fatal(err)
	}

	if err := st.CreateClient(&store.Client{
		InboundID: inID, Email: "bought-at-3am",
		UUID: "22222222-2222-2222-2222-222222222222",
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Now(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range clientsIn(t, cfg) {
		if e == "bought-at-3am" {
			found = true
		}
	}
	if !found {
		t.Error("a customer created after the last apply is missing from the core's config")
	}
}

// A config with no inbounds would take every customer offline at once.
// Leaving the previous one in place is the safer failure.
func TestRefusesToWriteAnEmptyConfig(t *testing.T) {
	st := newStore(t)
	inID := seed(t, st, "ali")
	cfg := filepath.Join(t.TempDir(), "xray.json")
	a := &Applier{Store: st, ConfigPath: cfg}
	if err := a.Now(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cfg)

	if err := st.SetInboundEnabled(inID, false); err != nil {
		t.Fatal(err)
	}
	if err := a.Now(); err == nil {
		t.Error("wrote a config with no inbound; everyone would have gone offline")
	}
	after, _ := os.ReadFile(cfg)
	if string(before) != string(after) {
		t.Error("the previous config was damaged by a failed apply")
	}
}

// A burst of changes must cost one reload, not one per change.
func TestBurstsAreCoalescedIntoOneReload(t *testing.T) {
	st := newStore(t)
	seed(t, st, "ali")
	cfg := filepath.Join(t.TempDir(), "xray.json")
	counter := filepath.Join(t.TempDir(), "count")
	a := &Applier{
		Store: st, ConfigPath: cfg, Debounce: 80 * time.Millisecond,
		Reload: []string{"sh", "-c", "echo x >> " + counter},
	}
	for i := 0; i < 20; i++ {
		a.Schedule()
	}
	time.Sleep(500 * time.Millisecond)

	raw, err := os.ReadFile(counter)
	if err != nil {
		t.Fatal("the reload never ran:", err)
	}
	if n := len(raw); n != 2 { // "x\n"
		t.Errorf("reload ran %d times for one burst, want once", n/2)
	}
}

func TestDisabledApplierDoesNothing(t *testing.T) {
	st := newStore(t)
	seed(t, st, "ali")
	var a *Applier
	a.Schedule() // must not panic on a nil applier
	if a.Enabled() {
		t.Error("a nil applier reports enabled")
	}
	b := &Applier{Store: st}
	if err := b.Now(); err != nil {
		t.Errorf("an applier with no config path should be a no-op, got %v", err)
	}
}

func TestReloadFailureIsReported(t *testing.T) {
	st := newStore(t)
	seed(t, st, "ali")
	a := &Applier{
		Store: st, ConfigPath: filepath.Join(t.TempDir(), "xray.json"),
		Reload: []string{"sh", "-c", "exit 3"},
	}
	if err := a.Now(); err == nil {
		t.Fatal("a reload that failed was reported as success")
	}
	_, lastErr, at := a.Status()
	if lastErr == nil || at.IsZero() {
		t.Error("the failure was not recorded for the operator to see")
	}
}
