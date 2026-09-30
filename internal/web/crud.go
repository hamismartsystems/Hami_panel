package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/provision"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
	tpl "github.com/hamismartsystems/hami_panel/internal/template"
)

// decode reads a JSON body, or a form body, into dst. Accepting both means
// the dashboard can post JSON while curl and scripts can post a plain form.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(dst)
	}
	if err := r.ParseForm(); err != nil {
		return err
	}
	m := map[string]any{}
	for k, v := range r.Form {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

// flexInt and flexFloat accept a number or a numeric string, because HTML
// form fields always arrive as strings.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexInt(int(n))
	return nil
}

type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexFloat(n)
	return nil
}

type flexBool bool

func (f *flexBool) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	*f = flexBool(s == "true" || s == "1" || s == "on" || s == "yes")
	return nil
}

/* ── templates ───────────────────────────────────────────────────────── */

func (s *Server) apiTemplates(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	all := tpl.All()
	out := make([]map[string]any, 0, len(all))
	for _, t := range all {
		out = append(out, map[string]any{
			"name": t.Name, "description": t.Description, "protocol": t.Protocol,
			"transport": t.Transport, "security": t.Security,
			"needs_sni": t.Security == "reality" || t.Security == "tls",
			"core":      coreOf(t.Protocol),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

func coreOf(protocol string) string {
	switch protocol {
	case "hysteria2", "tuic", "anytls":
		return "sing-box"
	default:
		return "xray"
	}
}

/* ── inbound create / delete ─────────────────────────────────────────── */

type inboundCreateReq struct {
	Template string   `json:"template"`
	Remark   string   `json:"remark"`
	Host     string   `json:"host"`
	Port     flexInt  `json:"port"`
	SNI      string   `json:"sni"`
	Dest     string   `json:"dest"`
	NodeID   flexInt  `json:"node_id"`
	Listen   string   `json:"listen"`
	CertFile string   `json:"cert_file"`
	KeyFile  string   `json:"key_file"`
	Private  flexBool `json:"private"`
}

func (s *Server) apiInboundCreate(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	var req inboundCreateReq
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}
	in, err := provision.Inbound(s.Store, provision.InboundOptions{
		Template: req.Template, Remark: req.Remark, Host: req.Host,
		Port: int(req.Port), SNI: req.SNI, Dest: req.Dest,
		NodeID: int64(req.NodeID), Listen: req.Listen,
		CertFile: req.CertFile, KeyFile: req.KeyFile, Private: bool(req.Private),
	})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	_ = s.Store.AddEvent("info", a.Username,
		"created inbound "+in.Remark+" on port "+strconv.Itoa(in.Port), "via=hp-ui")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "id": in.ID, "public_key": in.PublicKey, "short_id": in.ShortID,
	})
}

func (s *Server) apiInboundDelete(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	in, err := s.Store.GetInbound(id)
	if err != nil || in == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "inbound not found"})
		return
	}
	clients, _ := s.Store.ListClientsOf(id)
	if err := s.Store.DeleteInbound(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = s.Store.AddEvent("warn", a.Username,
		"deleted inbound "+in.Remark+" and its "+strconv.Itoa(len(clients))+" users", "via=hp-ui")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed_users": len(clients)})
}

/* ── user create / update / delete ───────────────────────────────────── */

type userCreateReq struct {
	InboundID flexInt   `json:"inbound_id"`
	Email     string    `json:"email"`
	QuotaGB   flexFloat `json:"quota_gb"`
	Days      flexInt   `json:"days"`
	Count     flexInt   `json:"count"`
	IPLimit   flexInt   `json:"ip_limit"`
}

func (s *Server) apiUserCreate(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	var req userCreateReq
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}
	count := int(req.Count)
	if count < 1 {
		count = 1
	}
	if count > 100 {
		writeJSON(w, http.StatusUnprocessableEntity,
			map[string]string{"error": "at most 100 users at a time"})
		return
	}

	created := make([]map[string]any, 0, count)
	for i := 0; i < count; i++ {
		email := strings.TrimSpace(req.Email)
		if count > 1 {
			email = email + "-" + strconv.Itoa(i+1)
		}
		c, err := provision.User(s.Store, provision.UserOptions{
			InboundID: int64(req.InboundID), Email: email,
			QuotaGB: float64(req.QuotaGB), Days: int(req.Days),
			IPLimit: int(req.IPLimit),
		})
		if err != nil {
			// Report what already succeeded so the operator is not left
			// guessing which half of a batch went through.
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
				"error": err.Error(), "created": created,
			})
			return
		}
		created = append(created, map[string]any{
			"id": c.ID, "email": c.Email, "sub_token": c.SubToken,
		})
	}
	_ = s.Store.AddEvent("info", a.Username,
		"created "+strconv.Itoa(len(created))+" user(s) starting with "+req.Email, "via=hp-ui")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created})
}

type userUpdateReq struct {
	QuotaGB *flexFloat `json:"quota_gb"`
	Days    *flexInt   `json:"days"`
	IPLimit *flexInt   `json:"ip_limit"`
	Unlimit flexBool   `json:"unlimited_time"`
}

func (s *Server) apiUserUpdate(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	c, err := s.Store.GetClient(id)
	if err != nil || c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	var req userUpdateReq
	if err := decode(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed request"})
		return
	}

	changes := []string{}
	if req.QuotaGB != nil {
		c.TotalBytes = provision.GBToBytes(float64(*req.QuotaGB))
		changes = append(changes, "quota")
	}
	if bool(req.Unlimit) {
		c.ExpireAt = nil
		changes = append(changes, "expiry cleared")
	} else if req.Days != nil {
		// Renewing always counts from now, so a lapsed user gets a full term.
		t := time.Now().UTC().Add(time.Duration(int(*req.Days)) * 24 * time.Hour)
		c.ExpireAt = &t
		changes = append(changes, "expiry")
	}
	if req.IPLimit != nil {
		c.IPLimit = int(*req.IPLimit)
		changes = append(changes, "device limit")
	}
	if len(changes) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "changed": 0})
		return
	}
	// A renewed or topped-up user must come back on.
	reactivated := false
	if !c.Enable {
		c.Enable = true
		reactivated = true
	}
	if err := s.Store.UpdateClient(c); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !c.Enable {
		_ = s.Store.SetClientEnabled(c.ID, true)
	}
	_ = s.Store.AddEvent("info", a.Username,
		"updated user "+c.Email+" ("+strings.Join(changes, ", ")+")", "via=hp-ui")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "changed": len(changes), "reactivated": reactivated,
	})
}

func (s *Server) apiUserDelete(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	c, err := s.Store.GetClient(id)
	if err != nil || c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	if err := s.Store.DeleteClient(id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = s.Store.AddEvent("warn", a.Username, "deleted user "+c.Email, "via=hp-ui")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// apiUserRotate issues a new subscription token. The old link stops working
// immediately, which is the point: it is how you cut off a leaked link.
func (s *Server) apiUserRotate(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	c, err := s.Store.GetClient(id)
	if err != nil || c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	token := subs.NewToken()
	if err := s.Store.RotateSubToken(id, token); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = s.Store.AddEvent("warn", a.Username,
		"rotated the subscription token of "+c.Email, "old link revoked")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sub_token": token})
}

// apiUserQR renders the share link as an SVG QR code, generated in-process:
// no third-party QR service ever sees a customer's key.
func (s *Server) apiUserQR(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	c, err := s.Store.GetClient(id)
	if err != nil || c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
		return
	}
	in, err := s.Store.GetInbound(c.InboundID)
	if err != nil || in == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "inbound not found"})
		return
	}
	raw, err := subs.LinkOf(subs.Entry{Inbound: *in, Client: *c})
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	svg, err := subs.QRSVG(raw)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email": c.Email, "link": raw, "svg": svg, "sub_token": c.SubToken,
	})
}
