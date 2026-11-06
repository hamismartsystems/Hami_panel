package web

import (
	"fmt"
	"net/http"

	"github.com/hamismartsystems/hami_panel/internal/actlog"
)

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// actionLog records every state-changing request (anything except GET and HEAD)
// with its path, status and client address. Request bodies are never logged.
func actionLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			return
		}
		if rec.code == 0 {
			rec.code = http.StatusOK
		}
		actlog.Log("hami-panel", fmt.Sprintf("%s %s status=%d from=%s", r.Method, r.URL.Path, rec.code, r.RemoteAddr))
	})
}
