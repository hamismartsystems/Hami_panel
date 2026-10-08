package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/apply"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// SessionCookie is the name of the HP-UI session cookie.
const SessionCookie = "hp_session"

// DefaultTTL is how long a session stays valid without "remember me".
const DefaultTTL = 12 * time.Hour

// RememberTTL is how long a "remember me" session stays valid.
const RememberTTL = 30 * 24 * time.Hour

// Server carries everything the HP-UI handlers need.
type Server struct {
	Store  *store.Store
	Secure bool // set true behind HTTPS so the cookie gets the Secure flag
	// DBPath is where Store keeps its file. The importer copies it aside
	// before writing, so an import can always be undone.
	DBPath string
	// BaseURL is the address customers use to fetch subscriptions. Empty
	// means "work it out from the request".
	BaseURL string
	// Apply pushes changes into the running core. Nil, or one with no
	// config path, means the panel only keeps records.
	Apply *apply.Applier
	// TrustedProxies lists the addresses of the reverse proxies allowed
	// to say who the visitor is through X-Forwarded-For. Empty — the
	// safe default — means the field is ignored and the socket address
	// is used, so a direct visitor cannot forge a fresh identity for
	// every login attempt and walk around the throttle.
	TrustedProxies *trustedProxies

	// live drives the per-user speed readout. Nil disables it: the panel
	// still works, the rates just stay at zero.
	live *liveSampler

	limiter loginLimiter
	imports importSessions
}

/* ── brute-force throttle ────────────────────────────────────────────── */

// loginLimiter slows down repeated failures from one address. It is a small
// in-memory counter, not a distributed rate limiter: it exists so a stolen
// username cannot be paired with a password list at full speed.
type loginLimiter struct {
	mu   sync.Mutex
	hits map[string]*limiterEntry
}

type limiterEntry struct {
	fails int
	until time.Time
}

const (
	maxFails    = 5
	lockoutBase = 30 * time.Second
	lockoutMax  = 15 * time.Minute
)

func (l *loginLimiter) blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.hits[key]
	if !ok {
		return false, 0
	}
	if d := time.Until(e.until); d > 0 {
		return true, d
	}
	return false, 0
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.hits == nil {
		l.hits = map[string]*limiterEntry{}
	}
	e, ok := l.hits[key]
	if !ok {
		e = &limiterEntry{}
		l.hits[key] = e
	}
	e.fails++
	if e.fails >= maxFails {
		d := lockoutBase * time.Duration(1<<uint(min(e.fails-maxFails, 5)))
		if d > lockoutMax {
			d = lockoutMax
		}
		e.until = time.Now().Add(d)
	}
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}

// trustedProxies decides whether a request's X-Forwarded-For may be
// believed. X-Forwarded-For is nothing but a header: any visitor can set
// it, and a throttle keyed on a value the visitor chooses is no throttle
// at all. So it is read only when the request arrived from an address the
// operator has explicitly named as a proxy we run.
type trustedProxies struct {
	nets []*net.IPNet
}

// ParseTrustedProxies reads "127.0.0.1/8,::1/128,10.0.0.0/8". A bare
// address is treated as a single host. Empty input means "trust nobody",
// which is the correct setting for a panel with no proxy in front.
func ParseTrustedProxies(spec string) (*trustedProxies, error) {
	tp := &trustedProxies{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			ip := net.ParseIP(part)
			if ip == nil {
				return nil, fmt.Errorf("trusted proxy %q is not an IP or CIDR", part)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			part = fmt.Sprintf("%s/%d", ip.String(), bits)
		}
		_, ipnet, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", part, err)
		}
		tp.nets = append(tp.nets, ipnet)
	}
	if len(tp.nets) == 0 {
		return nil, nil
	}
	return tp, nil
}

func (tp *trustedProxies) contains(ip net.IP) bool {
	if tp == nil || ip == nil {
		return false
	}
	for _, n := range tp.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// SetTrustedProxies installs the parsed allow-list. Nil means the panel
// sits directly on the network and no header is trusted.
func (s *Server) SetTrustedProxies(tp *trustedProxies) { s.TrustedProxies = tp }

func hostOf(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}

// clientKey is the throttle key for one request: the visitor's own
// address. Only when that address is a proxy we named ourselves does the
// last hop in X-Forwarded-For count, taking the rightmost entry because
// the loopback proxy appends the real peer there and a visitor can only
// add fields to the left.
func (s *Server) clientKey(r *http.Request) string {
	remote := hostOf(r.RemoteAddr)
	if s.TrustedProxies.contains(net.ParseIP(remote)) {
		if forwarded := forwardedFor(r.Header.Values("X-Forwarded-For")); forwarded != "" {
			return forwarded
		}
	}
	return remote
}

func forwardedFor(values []string) string {
	all := make([]string, 0, len(values))
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if p := strings.TrimSpace(part); p != "" {
				all = append(all, p)
			}
		}
	}
	if len(all) == 0 {
		return ""
	}
	last := all[len(all)-1]
	if net.ParseIP(last) == nil {
		// A malformed field must not become a throttle key of the
		// visitor's choosing.
		return ""
	}
	return last
}

/* ── handlers ────────────────────────────────────────────────────────── */

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := s.clientKey(r)
	if blocked, wait := s.limiter.blocked(key); blocked {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":       "too many attempts, try again later",
			"retry_after": int(wait.Seconds()),
		})
		return
	}

	var req loginRequest
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
			return
		}
		req.Username = r.FormValue("username")
		req.Password = r.FormValue("password")
		req.Remember = r.FormValue("remember") != ""
	}

	n, err := s.Store.CountAdmins()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	if n == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "no admin account yet — run: hami admin create -db panel.db -user NAME",
		})
		return
	}

	ttl := DefaultTTL
	if req.Remember {
		ttl = RememberTTL
	}
	sess, admin, err := s.Store.Authenticate(req.Username, req.Password, ttl)
	if err != nil {
		if errors.Is(err, store.ErrBadCredentials) {
			s.limiter.fail(key)
			_ = s.Store.AddEvent("warn", "hp-ui",
				"failed login for "+safeName(req.Username), "ip="+key)
			writeJSON(w, http.StatusUnauthorized,
				map[string]string{"error": "invalid username or password"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "database error"})
		return
	}
	s.limiter.reset(key)
	_ = s.Store.AddEvent("info", admin.Username, "signed in to HP-UI", "ip="+key)

	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    sess.Token,
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		Secure:   s.Secure,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"username": admin.Username,
		"redirect": "/hp-ui/",
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookie); err == nil {
		_ = s.Store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// adminOf returns the operator behind this request, or nil.
func (s *Server) adminOf(r *http.Request) *store.Admin {
	c, err := r.Cookie(SessionCookie)
	if err != nil {
		return nil
	}
	a, err := s.Store.SessionAdmin(c.Value)
	if err != nil {
		return nil
	}
	return a
}

// requireAdmin wraps an API handler so it only runs for a signed-in operator.
func (s *Server) requireAdmin(h func(http.ResponseWriter, *http.Request, *store.Admin)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := s.adminOf(r)
		if a == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "not signed in"})
			return
		}
		h(w, r, a)
	}
}

// requirePage wraps a page so an anonymous visitor is sent to the login form.
func (s *Server) requirePage(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.adminOf(r) == nil {
			http.Redirect(w, r, "/hp-ui/login", http.StatusFound)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// safeName keeps the audit log readable without echoing hostile input.
func safeName(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 40 {
		s = s[:40]
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r < 32 || r == 127 {
			r = '?'
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return "(empty)"
	}
	return string(out)
}
