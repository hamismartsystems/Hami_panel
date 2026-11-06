package subs

import (
	"fmt"
	"net/http"

	"github.com/hamismartsystems/hami_panel/internal/actlog"
)

// subErr writes an error response and records it. The request path is not logged
// because it contains the subscription token.
func subErr(r *http.Request, w http.ResponseWriter, msg string, code int) {
	actlog.Log("hami-sub", fmt.Sprintf("request %s refused status=%d reason=%q", r.Method, code, msg))
	http.Error(w, msg, code)
}
