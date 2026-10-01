package traffic

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func seedClient(t *testing.T, st *store.Store, email string, quota int64) store.Client {
	t.Helper()
	if existing, err := st.ListInbounds(); err == nil && len(existing) > 0 {
		c := &store.Client{
			InboundID: existing[0].ID,
			UUID:      "22222222-2222-2222-2222-2222222222" + pad(len(email)),
			Email:     email, TotalBytes: quota,
		}
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
		return *c
	}
	in := &store.Inbound{
		Remark: "in", Protocol: "vless", Port: 443, Host: "198.51.100.10",
		Transport: "tcp", Security: "none", Enable: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	c := &store.Client{
		InboundID: in.ID, UUID: "11111111-1111-1111-1111-11111111111" +
			string(rune('0'+len(email)%10)),
		Email: email, TotalBytes: quota,
	}
	if err := st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	return *c
}

func pad(n int) string {
	return string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}

func TestUsageAccumulatesAcrossRuns(t *testing.T) {
	st := openStore(t)
	seedClient(t, st, "ali", 0)
	now := time.Now()

	if _, err := Apply(st, []Sample{{Email: "ali", Up: 100, Down: 900}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(st, []Sample{{Email: "ali", Up: 50, Down: 450}}, now); err != nil {
		t.Fatal(err)
	}

	// each round is a delta, so the database holds the sum
	c, err := st.GetClientByEmail("ali")
	if err != nil {
		t.Fatal(err)
	}
	if c.UpBytes != 150 || c.DownBytes != 1350 {
		t.Errorf("usage = %d up / %d down, want 150 / 1350", c.UpBytes, c.DownBytes)
	}
	if c.LastSeen == nil {
		t.Error("a client that moved traffic has no last-seen stamp")
	}
}

func TestQuotaIsEnforcedNotJustDisplayed(t *testing.T) {
	st := openStore(t)
	seedClient(t, st, "reza", 1000)
	now := time.Now()

	res, err := Apply(st, []Sample{{Email: "reza", Up: 400, Down: 400}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Disabled) != 0 {
		t.Fatal("disabled a client that is still inside its quota")
	}
	c, _ := st.GetClientByEmail("reza")
	if !c.Enable {
		t.Fatal("client was switched off early")
	}

	res, err = Apply(st, []Sample{{Email: "reza", Up: 100, Down: 200}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Disabled) != 1 || res.Disabled[0] != "reza" {
		t.Errorf("crossing the quota did not switch the client off: %+v", res.Disabled)
	}
	c, _ = st.GetClientByEmail("reza")
	if c.Enable {
		t.Error("a client over quota is still enabled; the quota is decoration")
	}
}

func TestUnlimitedClientIsNeverDisabled(t *testing.T) {
	st := openStore(t)
	seedClient(t, st, "sara", 0) // 0 = unlimited
	res, err := Apply(st, []Sample{{Email: "sara", Up: 1 << 40, Down: 1 << 40}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Disabled) != 0 {
		t.Error("an unlimited client was disabled")
	}
}

func TestCountersForUnknownAccountsAreReportedNotSwallowed(t *testing.T) {
	st := openStore(t)
	seedClient(t, st, "ali", 0)
	res, err := Apply(st, []Sample{
		{Email: "ali", Up: 1, Down: 1},
		{Email: "ghost", Up: 500, Down: 500},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unknown) != 1 || res.Unknown[0] != "ghost" {
		t.Errorf("unknown accounts = %v; a counter the panel cannot place "+
			"usually means the core is running a config the panel did not write",
			res.Unknown)
	}
	if res.Matched != 1 {
		t.Errorf("matched = %d", res.Matched)
	}
}

func TestOnlineWindow(t *testing.T) {
	now := time.Now()
	recent := now.Add(-30 * time.Second)
	old := now.Add(-10 * time.Minute)

	if OnlineWithin(store.Client{}, 2*time.Minute, now) {
		t.Error("a client that was never seen must not count as online")
	}
	if !OnlineWithin(store.Client{LastSeen: &recent}, 2*time.Minute, now) {
		t.Error("seen thirty seconds ago should be online")
	}
	if OnlineWithin(store.Client{LastSeen: &old}, 2*time.Minute, now) {
		t.Error("seen ten minutes ago should not be online")
	}
}

// A collection that moved nothing must not keep rewriting rows: it would
// make every client look permanently online.
func TestIdleClientKeepsItsOldLastSeen(t *testing.T) {
	st := openStore(t)
	seedClient(t, st, "idle", 0)
	first := time.Now().Add(-time.Hour)
	if _, err := Apply(st, []Sample{{Email: "idle", Up: 10, Down: 10}}, first); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetClientByEmail("idle")

	if _, err := Apply(st, []Sample{{Email: "idle"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetClientByEmail("idle")
	if after.LastSeen == nil || before.LastSeen == nil {
		t.Fatal("missing stamps")
	}
	if !after.LastSeen.Equal(*before.LastSeen) {
		t.Errorf("an idle round moved last-seen from %v to %v", before.LastSeen, after.LastSeen)
	}
}
