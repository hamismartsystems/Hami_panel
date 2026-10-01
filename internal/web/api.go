package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

/* ── view models ─────────────────────────────────────────────────────── */

type inboundView struct {
	ID        int64  `json:"id"`
	Remark    string `json:"remark"`
	Protocol  string `json:"protocol"`
	Port      int    `json:"port"`
	Host      string `json:"host"`
	Transport string `json:"transport"`
	Security  string `json:"security"`
	SNI       string `json:"sni"`
	NodeID    int64  `json:"node_id"`
	NodeName  string `json:"node_name"`
	Private   bool   `json:"private"`
	Enabled   bool   `json:"enabled"`
	Clients   int    `json:"clients"`
	// The rest is what the edit dialog needs to show current values.
	Fingerprint string `json:"fingerprint"`
	Path        string `json:"path"`
	XHTTPMode   string `json:"xhttp_mode"`
	HeaderType  string `json:"header_type"`
	Flow        string `json:"flow"`
	Dest        string `json:"dest"`
}

type userView struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	InboundID int64  `json:"inbound_id"`
	Inbound   string `json:"inbound"`
	Enabled   bool   `json:"enabled"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	Total     int64  `json:"total"`
	Quota     int64  `json:"quota"`
	ExpireAt  string `json:"expire_at"`
	DaysLeft  *int   `json:"days_left"`
	Expired   bool   `json:"expired"`
	OverQuota bool   `json:"over_quota"`
	SubToken  string `json:"sub_token"`
	// Online is derived from the last byte seen, not from a live socket:
	// the core reports usage on an interval, so this is "active very
	// recently" rather than "connected this instant".
	Online    bool   `json:"online"`
	LastSeen  string `json:"last_seen"`
	Flow      string `json:"flow"`
	IPLimit   int    `json:"ip_limit"`
	CreatedAt string `json:"created_at"`
}

type nodeView struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Address  string `json:"address"`
	Status   string `json:"status"`
	LastSeen string `json:"last_seen"`
	Inbounds int    `json:"inbounds"`
}

type eventView struct {
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Actor   string `json:"actor"`
	Message string `json:"message"`
	Meta    string `json:"meta"`
}

/* ── collection ──────────────────────────────────────────────────────── */

func (s *Server) nodeNames() map[int64]string {
	names := map[int64]string{0: "this server"}
	if nodes, err := s.Store.ListNodes(); err == nil {
		for _, n := range nodes {
			names[n.ID] = n.Name
		}
	}
	return names
}

func (s *Server) inboundViews() ([]inboundView, map[int64]string, error) {
	ins, err := s.Store.ListInbounds()
	if err != nil {
		return nil, nil, err
	}
	names := s.nodeNames()
	titles := make(map[int64]string, len(ins))
	out := make([]inboundView, 0, len(ins))
	for _, in := range ins {
		clients, _ := s.Store.ListClientsOf(in.ID)
		nodeName := names[in.NodeID]
		if nodeName == "" {
			nodeName = "node " + strconv.FormatInt(in.NodeID, 10)
		}
		titles[in.ID] = in.Remark
		// The handshake target lives with the secrets; the edit dialog
		// needs it, and it is not itself a secret.
		dest := ""
		if sec, err := s.Store.GetInboundSecret(in.ID); err == nil {
			dest = sec.Dest
		}
		out = append(out, inboundView{
			ID: in.ID, Remark: in.Remark, Protocol: in.Protocol, Port: in.Port,
			Host: in.Host, Transport: in.Transport, Security: in.Security, SNI: in.SNI,
			NodeID: in.NodeID, NodeName: nodeName, Private: in.IsPrivate,
			Enabled: in.Enable, Clients: len(clients),
			Fingerprint: in.Fingerprint, Path: in.Path, XHTTPMode: in.XHTTPMode,
			HeaderType: in.HeaderType, Flow: in.Flow, Dest: dest,
		})
	}
	return out, titles, nil
}

// OnlineWindow is how long after its last byte a client still counts as
// online. It has to be comfortably longer than the collection interval,
// or everyone blinks offline between runs.
const OnlineWindow = 3 * time.Minute

func userViewOf(c store.Client, inboundName string, now time.Time) userView {
	v := userView{
		ID: c.ID, Email: c.Email, InboundID: c.InboundID, Inbound: inboundName,
		Enabled: c.Enable, Up: c.UpBytes, Down: c.DownBytes,
		Total: c.UpBytes + c.DownBytes, Quota: c.TotalBytes, SubToken: c.SubToken,
		Flow: c.Flow, IPLimit: c.IPLimit,
		CreatedAt: c.CreatedAt.UTC().Format(time.RFC3339),
	}
	if c.LastSeen != nil && !c.LastSeen.IsZero() {
		v.LastSeen = c.LastSeen.UTC().Format(time.RFC3339)
		v.Online = now.Sub(*c.LastSeen) <= OnlineWindow
	}
	if c.TotalBytes > 0 && v.Total >= c.TotalBytes {
		v.OverQuota = true
	}
	if c.ExpireAt != nil && !c.ExpireAt.IsZero() {
		v.ExpireAt = c.ExpireAt.UTC().Format(time.RFC3339)
		d := int(c.ExpireAt.Sub(now).Hours() / 24)
		if d < 0 {
			d = 0
		}
		v.DaysLeft = &d
		v.Expired = now.After(*c.ExpireAt)
	}
	return v
}

/* ── endpoints ───────────────────────────────────────────────────────── */

func (s *Server) apiMe(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	last := ""
	if a.LastLogin != nil {
		last = a.LastLogin.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username": a.Username, "last_login": last,
		"created_at": a.CreatedAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) apiOverview(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	ins, titles, err := s.inboundViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	clients, err := s.Store.ListAllClients()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now().UTC()
	var up, down int64
	var active, expiring, exhausted int
	attention := make([]userView, 0, 8)
	for _, c := range clients {
		up += c.UpBytes
		down += c.DownBytes
		v := userViewOf(c, titles[c.InboundID], now)
		if v.Enabled && !v.Expired && !v.OverQuota {
			active++
		}
		if v.Expired || v.OverQuota {
			exhausted++
			if len(attention) < 8 {
				attention = append(attention, v)
			}
		} else if v.DaysLeft != nil && *v.DaysLeft <= 7 {
			expiring++
			if len(attention) < 8 {
				attention = append(attention, v)
			}
		}
	}
	nodes, _ := s.Store.ListNodes()
	nodesUp := 0
	for _, n := range nodes {
		if n.Status == "up" {
			nodesUp++
		}
	}
	enabledIn := 0
	for _, in := range ins {
		if in.Enabled {
			enabledIn++
		}
	}
	events, _ := s.Store.RecentEvents(10)
	writeJSON(w, http.StatusOK, map[string]any{
		"inbounds":         len(ins),
		"inbounds_enabled": enabledIn,
		"users":            len(clients),
		"users_active":     active,
		"users_expiring":   expiring,
		"users_exhausted":  exhausted,
		"nodes":            len(nodes),
		"nodes_up":         nodesUp,
		"up":               up,
		"down":             down,
		"attention":        attention,
		"events":           eventViews(events),
	})
}

func (s *Server) apiInbounds(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	ins, _, err := s.inboundViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inbounds": ins})
}

func (s *Server) apiUsers(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	_, titles, err := s.inboundViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	clients, err := s.Store.ListAllClients()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	now := time.Now().UTC()
	out := make([]userView, 0, len(clients))
	for _, c := range clients {
		out = append(out, userViewOf(c, titles[c.InboundID], now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) apiNodes(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	nodes, err := s.Store.ListNodes()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ins, _ := s.Store.ListInbounds()
	count := map[int64]int{}
	for _, in := range ins {
		count[in.NodeID]++
	}
	out := make([]nodeView, 0, len(nodes)+1)
	out = append(out, nodeView{ID: 0, Name: "this server", Address: "local",
		Status: "up", Inbounds: count[0]})
	for _, n := range nodes {
		last := ""
		if n.LastSeen != nil {
			last = n.LastSeen.UTC().Format(time.RFC3339)
		}
		st := n.Status
		if st == "" {
			st = "unknown"
		}
		out = append(out, nodeView{ID: n.ID, Name: n.Name, Address: n.Address,
			Status: st, LastSeen: last, Inbounds: count[n.ID]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	limit := 100
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	events, err := s.Store.RecentEvents(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": eventViews(events)})
}

// apiUserLink returns the share link of one client, built from that client's
// own inbound only — the panel never merges settings from another inbound.
func (s *Server) apiUserLink(w http.ResponseWriter, r *http.Request, a *store.Admin) {
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
	writeJSON(w, http.StatusOK, map[string]any{
		"link": raw, "email": c.Email, "sub_token": c.SubToken,
	})
}

/* ── mutations ───────────────────────────────────────────────────────── */

func (s *Server) apiUserToggle(w http.ResponseWriter, r *http.Request, a *store.Admin) {
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
	if err := s.Store.SetClientEnabled(id, !c.Enable); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	verb := "disabled"
	if !c.Enable {
		verb = "enabled"
	}
	_ = s.Store.AddEvent("info", a.Username, verb+" user "+c.Email, "via=hp-ui")
	s.Apply.Schedule()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": !c.Enable})
}

func (s *Server) apiUserReset(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id"})
		return
	}
	up, down, err := s.Store.ResetClientUsage(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	_ = s.Store.AddEvent("info", a.Username, "reset usage of user "+strconv.FormatInt(id, 10),
		"via=hp-ui")
	s.Apply.Schedule()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "was_up": up, "was_down": down})
}

func (s *Server) apiInboundToggle(w http.ResponseWriter, r *http.Request, a *store.Admin) {
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
	if err := s.Store.SetInboundEnabled(id, !in.Enable); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	verb := "disabled"
	if !in.Enable {
		verb = "enabled"
	}
	_ = s.Store.AddEvent("info", a.Username, verb+" inbound "+in.Remark, "via=hp-ui")
	s.Apply.Schedule()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": !in.Enable})
}

/* ── helpers ─────────────────────────────────────────────────────────── */

func eventViews(events []store.Event) []eventView {
	out := make([]eventView, 0, len(events))
	for _, e := range events {
		out = append(out, eventView{
			TS: e.TS.UTC().Format(time.RFC3339), Level: e.Level,
			Actor: e.Actor, Message: e.Message, Meta: e.Meta,
		})
	}
	return out
}
