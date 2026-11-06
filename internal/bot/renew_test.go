package bot

import (
	"os"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func TestRenewalAddsToTheRemainingDays(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	end := now.Add(5 * 24 * time.Hour)
	got := renewedExpiry(&store.Client{ExpireAt: &end}, now)
	want := end.Add(31 * 24 * time.Hour)
	if got == nil || !got.Equal(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRenewalOfAnExpiredServiceStartsFromToday(t *testing.T) {
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	end := now.Add(-10 * 24 * time.Hour)
	got := renewedExpiry(&store.Client{ExpireAt: &end}, now)
	if got == nil || !got.Equal(now.Add(31*24*time.Hour)) {
		t.Fatalf("got %v", got)
	}
}

func TestRenewalKeepsAPendingFirstUseClock(t *testing.T) {
	now := time.Now()
	if got := renewedExpiry(&store.Client{ActivateDays: 31}, now); got != nil {
		t.Fatalf("a clock that has not started must stay unstarted, got %v", got)
	}
}

func TestSweepRemovesOnlyBotServicesPastGrace(t *testing.T) {
	st := openStore(t)
	now := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	in := &store.Inbound{Remark: "test", Protocol: "vless", Port: 443, Host: "test.invalid"}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	mk := func(email string, expire time.Time) int64 {
		c := &store.Client{InboundID: in.ID, UUID: "u-" + email, Email: email, Enable: true,
			SubToken: "t-" + email, ExpireAt: &expire}
		if err := st.CreateClient(c); err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	old := mk("bot-old", now.Add(-4*24*time.Hour))     // past grace: remove
	fresh := mk("bot-fresh", now.Add(-2*24*time.Hour)) // inside grace: keep
	manual := mk("manual", now.Add(-30*24*time.Hour))  // not sold by bot: keep
	st.UpsertBotUser(501, "b", "B")
	for _, p := range []struct {
		id    int64
		email string
	}{{old, "bot-old"}, {fresh, "bot-fresh"}} {
		o := &store.Order{TelegramID: 501, Plan: "monthly", GB: 5, Price: 1, ListPrice: 1}
		if err := st.CreateOrder(o); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkOrder(o.ID, store.OrderPaid, "card"); err != nil {
			t.Fatal(err)
		}
		if err := st.AttachOrderClient(o.ID, p.id, p.email); err != nil {
			t.Fatal(err)
		}
	}
	got, err := SweepBotExpired(st, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "bot-old" {
		t.Fatalf("removed %v, want [bot-old]", got)
	}
	if c, _ := st.GetClient(old); c != nil {
		t.Error("bot-old should be gone")
	}
	if c, _ := st.GetClient(fresh); c == nil {
		t.Error("bot-fresh is inside its grace period and must stay")
	}
	if c, _ := st.GetClient(manual); c == nil {
		t.Error("a non-bot account must never be removed")
	}
}

// Dry run against a copy of the real panel database. Skipped unless
// HAMI_SWEEP_DB points at a copy, so normal test runs never touch data.
func TestSweepDryRunOnACopy(t *testing.T) {
	path := os.Getenv("HAMI_SWEEP_DB")
	if path == "" {
		t.Skip("set HAMI_SWEEP_DB to a copy of panel.db to run")
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := SweepBotExpired(st, time.Now().UTC(), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dry run would remove %d: %v", len(got), got)
}
