package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

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

func clientKey(r *http.Request) string {
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if i := strings.IndexByte(xf, ','); i > 0 {
			return strings.TrimSpace(xf[:i])
		}
		return strings.TrimSpace(xf)
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}

/* ── handlers ────────────────────────────────────────────────────────── */

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := clientKey(r)
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
