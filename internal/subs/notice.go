package subs

import (
	"fmt"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// AlertKind classifies an end-of-service warning.
type AlertKind string

const (
	AlertExpired    AlertKind = "expired"
	AlertQuotaFull  AlertKind = "quota_full"
	AlertExpirySoon AlertKind = "expiry_soon"
	AlertQuotaHigh  AlertKind = "quota_high"
)

// Alert is one due notification for one client.
type Alert struct {
	Email  string
	Kind   AlertKind
	Detail string
}

// DueAlerts computes which clients need a warning right now:
//   - service already stopped (expired / quota exhausted)
//   - expiry within `days`
//   - usage at ≥ `ratio` of quota (0 < ratio < 1)
//
// Dead accounts are always alert-worthy exactly once per computation;
// it is the caller (cron + audit log) to de-duplicate in time.
func DueAlerts(clients []store.Client, now time.Time, days int, ratio float64) []Alert {
	var out []Alert
	for _, c := range clients {
		if !c.Enable {
			continue // admin's choice, not a warnable event
		}
		remaining := c.TotalBytes - c.UpBytes - c.DownBytes
		if c.TotalBytes > 0 {
			if remaining <= 0 {
				out = append(out, Alert{c.Email, AlertQuotaFull,
					fmt.Sprintf("used %s of %s", humanBytes(c.UpBytes+c.DownBytes), humanBytes(c.TotalBytes))})
			} else if float64(c.UpBytes+c.DownBytes) >= ratio*float64(c.TotalBytes) {
				out = append(out, Alert{c.Email, AlertQuotaHigh,
					fmt.Sprintf("%s left of %s", humanBytes(remaining), humanBytes(c.TotalBytes))})
			}
		}
		if c.ExpireAt != nil {
			until := c.ExpireAt.UTC().Sub(now)
			switch {
			case until <= 0:
				out = append(out, Alert{c.Email, AlertExpired,
					"expired " + c.ExpireAt.UTC().Format("2006-01-02")})
			case until <= time.Duration(days)*24*time.Hour:
				out = append(out, Alert{c.Email, AlertExpirySoon,
					fmt.Sprintf("%s left, expires %s", humanDuration(until), c.ExpireAt.UTC().Format("2006-01-02"))})
			}
		}
	}
	return out
}

func humanDuration(d time.Duration) string {
	days := int(d.Hours() / 24)
	if days > 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
