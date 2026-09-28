package subs

import (
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func TestDueAlerts(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	inDays := func(d int) *time.Time { t := now.Add(time.Duration(d) * 24 * time.Hour); return &t }

	clients := []store.Client{
		{Email: "fresh", Enable: true, TotalBytes: 10 << 30, UpBytes: 1 << 30, ExpireAt: inDays(30)},
		// expiring in 2 days (window 3)
		{Email: "expiring", Enable: true, ExpireAt: inDays(2)},
		// expired
		{Email: "dead", Enable: true, ExpireAt: inDays(-1)},
		// 85% used of 100 GB (ratio .8) → warning
		{Email: "thirsty", Enable: true, TotalBytes: 100 << 30, UpBytes: 40 << 30, DownBytes: 45 << 30},
		// over quota
		{Email: "full", Enable: true, TotalBytes: 1 << 30, UpBytes: 1 << 30},
		// disabled: never alerts
		{Email: "off", Enable: false, TotalBytes: 1 << 30, UpBytes: 5 << 30, ExpireAt: inDays(-100)},
		// unlimited quota: no quota alerts ever
		{Email: "vip", Enable: true, ExpireAt: inDays(60)},
	}

	alerts := DueAlerts(clients, now, 3, 0.8)
	byEmail := map[string][]AlertKind{}
	for _, a := range alerts {
		byEmail[a.Email] = append(byEmail[a.Email], a.Kind)
	}
	if len(byEmail["fresh"]) != 0 {
		t.Fatalf("fresh client must not alert: %v", byEmail["fresh"])
	}
	if len(byEmail["expiring"]) != 1 || byEmail["expiring"][0] != AlertExpirySoon {
		t.Fatalf("expiring: %v", byEmail["expiring"])
	}
	if len(byEmail["dead"]) != 1 || byEmail["dead"][0] != AlertExpired {
		t.Fatalf("dead: %v", byEmail["dead"])
	}
	if len(byEmail["thirsty"]) != 1 || byEmail["thirsty"][0] != AlertQuotaHigh {
		t.Fatalf("thirsty: %v", byEmail["thirsty"])
	}
	if len(byEmail["full"]) != 1 || byEmail["full"][0] != AlertQuotaFull {
		t.Fatalf("full: %v", byEmail["full"])
	}
	if len(byEmail["off"]) != 0 {
		t.Fatalf("disabled must never alert: %v", byEmail["off"])
	}
	if len(byEmail["vip"]) != 0 {
		t.Fatalf("unlimited must not quota-alert: %v", byEmail["vip"])
	}
}
