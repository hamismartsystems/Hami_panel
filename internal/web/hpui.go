package web

import (
	_ "embed"
	"net/http"
)

//go:embed login.html
var loginHTML string

// Handler returns the HP-UI web handler.
// Routes:
//   GET /          -> redirect to /hp-ui/login
//   GET /hp-ui/login -> login page (HP-UI / HAMI PANEL)
//   POST /api/login -> placeholder (returns 501 until auth is implemented)
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/hp-ui/login", http.StatusFound)
	})
	mux.HandleFunc("GET /hp-ui/login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Write([]byte(loginHTML))
	})
	mux.HandleFunc("POST /api/login", func(w http.ResponseWriter, r *http.Request) {
		// TODO: implement real auth against store or env
		// For now, return 501 to indicate not yet implemented
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte(`{"error":"login not yet implemented — HP-UI auth coming in next commit"}`))
	})
	return mux
}
