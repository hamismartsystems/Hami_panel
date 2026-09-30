package web

import (
	_ "embed"
	"net/http"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

//go:embed login.html
var loginHTML string

//go:embed dashboard.html
var dashboardHTML string

// Handler returns the HP-UI web handler.
//
// Public:
//
//	GET  /                      -> redirect to the dashboard (or the login page)
//	GET  /hp-ui/login           -> login page
//	POST /api/login             -> sign in, sets the session cookie
//	POST /api/logout            -> sign out
//
// Signed in only:
//
//	GET  /hp-ui/                -> dashboard
//	GET  /api/me                -> current operator
//	GET  /api/overview          -> counters, attention list, recent events
//	GET  /api/inbounds          -> inbound table
//	GET  /api/users             -> user table
//	GET  /api/nodes             -> node table
//	GET  /api/events            -> audit log
//	GET  /api/users/{id}/link   -> share link of one user
//	POST /api/users/{id}/toggle -> enable/disable a user
//	POST /api/users/{id}/reset  -> zero a user's usage
//	POST /api/inbounds/{id}/toggle -> enable/disable an inbound
//	GET  /api/templates         -> built-in inbound templates
//	GET  /api/users/{id}/qr     -> share link as an SVG QR code
//	POST /api/inbounds          -> create an inbound from a template
//	POST /api/inbounds/{id}/delete -> delete an inbound and its users
//	POST /api/users             -> create one or many users
//	POST /api/users/{id}/update -> change quota, expiry or device limit
//	POST /api/users/{id}/delete -> delete a user
//	POST /api/users/{id}/rotate -> issue a new subscription token
func Handler(st *store.Store) http.Handler {
	return (&Server{Store: st}).Routes()
}

// Routes builds the mux for a configured Server (lets callers set Secure).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if s.adminOf(r) == nil {
			http.Redirect(w, r, "/hp-ui/login", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/hp-ui/", http.StatusFound)
	})

	mux.HandleFunc("GET /hp-ui/login", func(w http.ResponseWriter, r *http.Request) {
		if s.adminOf(r) != nil {
			http.Redirect(w, r, "/hp-ui/", http.StatusFound)
			return
		}
		writeHTML(w, loginHTML)
	})

	mux.HandleFunc("GET /hp-ui/", s.requirePage(func(w http.ResponseWriter, r *http.Request) {
		writeHTML(w, dashboardHTML)
	}))

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.HandleFunc("GET /api/me", s.requireAdmin(s.apiMe))
	mux.HandleFunc("GET /api/overview", s.requireAdmin(s.apiOverview))
	mux.HandleFunc("GET /api/inbounds", s.requireAdmin(s.apiInbounds))
	mux.HandleFunc("GET /api/users", s.requireAdmin(s.apiUsers))
	mux.HandleFunc("GET /api/nodes", s.requireAdmin(s.apiNodes))
	mux.HandleFunc("GET /api/events", s.requireAdmin(s.apiEvents))
	mux.HandleFunc("GET /api/users/{id}/link", s.requireAdmin(s.apiUserLink))
	mux.HandleFunc("GET /api/users/{id}/qr", s.requireAdmin(s.apiUserQR))
	mux.HandleFunc("GET /api/templates", s.requireAdmin(s.apiTemplates))

	mux.HandleFunc("POST /api/users/{id}/toggle", s.requireAdmin(s.apiUserToggle))
	mux.HandleFunc("POST /api/users/{id}/reset", s.requireAdmin(s.apiUserReset))
	mux.HandleFunc("POST /api/inbounds/{id}/toggle", s.requireAdmin(s.apiInboundToggle))
	mux.HandleFunc("POST /api/inbounds", s.requireAdmin(s.apiInboundCreate))
	mux.HandleFunc("POST /api/inbounds/{id}/delete", s.requireAdmin(s.apiInboundDelete))
	mux.HandleFunc("POST /api/users", s.requireAdmin(s.apiUserCreate))
	mux.HandleFunc("POST /api/users/{id}/update", s.requireAdmin(s.apiUserUpdate))
	mux.HandleFunc("POST /api/users/{id}/delete", s.requireAdmin(s.apiUserDelete))
	mux.HandleFunc("POST /api/users/{id}/rotate", s.requireAdmin(s.apiUserRotate))

	return securityHeaders(mux)
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}

// securityHeaders applies a strict, self-contained policy. The pages ship no
// external assets, so a tight CSP costs nothing and blocks injected scripts.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'self'")
		next.ServeHTTP(w, r)
	})
}
