package subs

import (
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// Server serves subscription links and customer status pages.
type Server struct {
	Store *store.Store
	// BaseURL is the public prefix, e.g. "https://panel.example.com" —
	// no trailing slash. Used to print the QR target on the status page.
	BaseURL string
	// Now is overridable for tests.
	Now func() time.Time
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Handler is the root http.Handler; mount under your choice of prefix.
// Routes:
//
//	GET /sub/{token}          subscription in the requested format
//	GET /sub/{token}/status   human-readable status page with QR
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sub/{token}", s.handleSub)
	mux.HandleFunc("GET /sub/{token}/status", s.handleStatus)
	return mux
}

// SubURL returns the full subscription URL for a token.
func (s *Server) SubURL(token string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/sub/" + token
}

func (s *Server) handleSub(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	c, err := s.Store.ClientBySubToken(token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown subscription", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	now := s.now()
	entries, err := Entries(s.Store, *c, now)
	if err != nil {
		http.Error(w, "config error", http.StatusInternalServerError)
		return
	}

	format := FormatFor(r.UserAgent(), r.URL.Query().Get("format"))
	body, err := Render(entries, format)
	if err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", ContentTypeMap[format])
	w.Header().Set("Subscription-Userinfo", UserInfoHeader(*c))
	if c.ExpireAt != nil {
		w.Header().Set("Profile-Expire", c.ExpireAt.UTC().Format(http.TimeFormat))
	}
	w.Header().Set("Profile-Update-Interval", "12")
	w.Header().Set("Profile-Web-Page-Url", s.SubURL(token)+"/status")
	// clients that get zero links must not cache an empty list for hours
	if len(entries) == 0 {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.Write(body)
}

/* ── status page ─────────────────────────────────────────────────────── */

func humanBytes(n int64) string {
	const unit = 1 << 30 // GB only; enough for quota display
	if n >= unit {
		return fmt.Sprintf("%.2f GB", float64(n)/float64(unit))
	}
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	}
	return fmt.Sprintf("%d B", n)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	c, err := s.Store.ClientBySubToken(token)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown subscription", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := s.now()
	active := ActiveNow(*c, now)
	subURL := s.SubURL(token)

	var qrBlock string
	if active {
		if svg, err := QRSVG(subURL); err == nil {
			qrBlock = `<img alt="QR" width="220" height="220" src="data:image/svg+xml;base64,` +
				base64.StdEncoding.EncodeToString([]byte(svg)) + `">`
		}
	}

	quotaLine := "∞"
	if c.TotalBytes > 0 {
		quotaLine = fmt.Sprintf("%s / %s", humanBytes(c.UpBytes+c.DownBytes), humanBytes(c.TotalBytes))
	}
	expireLine := "—"
	if c.ExpireAt != nil {
		expireLine = c.ExpireAt.UTC().Format("2006-01-02")
	}
	status, cls := "فعّال", "ok"
	if !active {
		status = map[string]string{
			"disabled":        "غیرفعّال",
			"expired":         "منقضی",
			"quota exhausted": "حجم تمام‌شده",
		}[WhyInactive(*c, now)]
		if status == "" {
			status = "غیرفعّال"
		}
		cls = "bad"
	}

	page := fmt.Sprintf(`<!DOCTYPE html><html lang="fa" dir="rtl"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>وضعیت اشتراک</title>
<style>
body{font-family:Tahoma,sans-serif;background:#0b1020;color:#eef2ff;max-width:420px;margin:24px auto;padding:16px;text-align:center}
.card{background:#141b33;border:1px solid #26304f;border-radius:16px;padding:20px}
.ok{color:#2fd47b}.bad{color:#ff6b6b;font-weight:700}
table{margin:14px auto;font-size:14px}td{padding:5px 10px;text-align:right}
.url{direction:ltr;word-break:break-all;font-family:monospace;font-size:11px;background:#0e1528;border-radius:8px;padding:8px;margin-top:14px}
</style></head><body><div class="card">
<h2>وضعیت اشتراک Scorpion</h2>
<p class="%s">%s</p>
%s
<table>
<tr><td>حجم مصرف‌شده</td><td>%s</td></tr>
<tr><td>تاریخ انقضا</td><td>%s</td></tr>
</table>
<div class="url">%s</div>
</div></body></html>`, cls, status, qrBlock, quotaLine, expireLine, html.EscapeString(subURL))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(page))
}
