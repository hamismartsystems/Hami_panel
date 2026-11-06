package bot

import (
	"context"
	"fmt"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// SweepBotExpired removes services that were sold by the sales bot and
// stayed unrenewed for GraceAfterExpiry after they ended. Only accounts
// that appear in bot_orders are considered. Manual, reseller and bulk
// accounts are never touched. With dryRun it only lists them.
func SweepBotExpired(st *store.Store, now time.Time, dryRun bool) ([]string, error) {
	orders, err := st.DeliveredBotOrders()
	if err != nil {
		return nil, err
	}
	seen := map[int64]bool{}
	var done []string
	for _, o := range orders {
		if seen[o.ClientID] {
			continue
		}
		seen[o.ClientID] = true
		c, err := st.GetClient(o.ClientID)
		if err != nil || c == nil {
			continue // already gone
		}
		if c.ExpireAt == nil {
			continue // clock not started; nothing to expire
		}
		if now.Before(c.ExpireAt.Add(GraceAfterExpiry)) {
			continue // still inside the grace period, or still running
		}
		if !dryRun {
			if err := st.DeleteClient(c.ID); err != nil {
				return done, fmt.Errorf("delete client %d: %w", c.ID, err)
			}
			_ = st.AddEvent("warn", "bot", "removed an unrenewed bot service after the grace period", c.Email)
		}
		done = append(done, c.Email)
	}
	return done, nil
}

// sweepLoop runs the sweep once a day, between 03:00 and 03:59 Tehran.
func (b *Bot) sweepLoop(ctx context.Context) {
	loc := time.FixedZone("Tehran", 3*3600+30*60)
	var lastDay string
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now()
		local := now.In(loc)
		day := local.Format("2006-01-02")
		if local.Hour() != 3 || day == lastDay {
			continue
		}
		lastDay = day
		b.runSweep(ctx, now)
	}
}

func (b *Bot) runSweep(ctx context.Context, now time.Time) {
	removed, err := SweepBotExpired(b.cfg.Store, now.UTC(), false)
	if err != nil {
		alog("sweep failed: %v", err)
		_ = b.cfg.Store.AddEvent("error", "bot", "daily sweep failed", err.Error())
		return
	}
	alog("sweep removed=%d", len(removed))
	if len(removed) == 0 {
		return
	}
	if b.cfg.Apply.Enabled() {
		if err := b.cfg.Apply.Now(); err != nil {
			_ = b.cfg.Store.AddEvent("error", "bot", "the core did not pick up the sweep", err.Error())
		}
	}
	if b.cfg.AdminID != 0 {
		_ = b.api.send(ctx, b.cfg.AdminID,
			fmt.Sprintf("حذف خودکار: %d سرویس فروش ربات که بعد از ۳ روز تمدید نشدند، از سرور حذف شدند.", len(removed)), "")
	}
}
