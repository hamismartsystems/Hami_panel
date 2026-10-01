package importer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// xuiSource reads the x-ui family: MHSanaei's 3x-ui, alireza0's fork, and
// the original vaxilu x-ui. They share one schema that grew over time, so
// one adapter handles all three and reports which shape it found.
//
// Two client layouts exist and both are supported:
//
//   - clients embedded as JSON inside inbounds.settings — every version
//   - a normalised clients + client_inbounds pair — recent 3x-ui
//
// When both are present the normalised tables win, because that is what
// the panel itself treats as authoritative, and the JSON can lag behind.
type xuiSource struct{ kind Kind }

func (x *xuiSource) Kind() Kind { return x.kind }

func (x *xuiSource) Detect(db *sql.DB) (bool, string) {
	if !tableExists(db, "inbounds") || !columnExists(db, "inbounds", "stream_settings") {
		return false, ""
	}
	switch {
	case tableExists(db, "clients") && tableExists(db, "client_inbounds"):
		x.kind = Kind3xUI
		return true, "3x-ui, normalised clients"
	case tableExists(db, "client_traffics"):
		x.kind = Kind3xUI
		return true, "3x-ui or alireza fork, clients in inbound settings"
	default:
		x.kind = KindXUI
		return true, "legacy x-ui, clients in inbound settings, no traffic table"
	}
}

func (x *xuiSource) Read(db *sql.DB, opt Options) (*Snapshot, error) {
	snap := &Snapshot{}

	normalised := tableExists(db, "clients") && tableExists(db, "client_inbounds")
	traffic, err := x.readTraffic(db)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`SELECT id, remark, enable, port, protocol,
		COALESCE(listen,''), COALESCE(settings,''), COALESCE(stream_settings,''),
		COALESCE(tag,'') FROM inbounds ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading inbounds: %w", err)
	}
	defer rows.Close()

	type raw struct {
		id                       int64
		remark, protocol, listen string
		settings, stream, tag    string
		enable                   bool
		port                     int
	}
	var list []raw
	for rows.Next() {
		var r raw
		if err := rows.Scan(&r.id, &r.remark, &r.enable, &r.port, &r.protocol,
			&r.listen, &r.settings, &r.stream, &r.tag); err != nil {
			return nil, fmt.Errorf("reading inbounds: %w", err)
		}
		list = append(list, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, r := range list {
		ref := fmt.Sprintf("inbound %d %q", r.id, r.remark)

		proto := strings.ToLower(strings.TrimSpace(r.protocol))
		if !supportedProtocol(proto) {
			snap.Skipped = append(snap.Skipped, Skip{
				What: "inbound", Ref: ref,
				Reason: fmt.Sprintf("protocol %q is not something HAMI serves", proto),
			})
			continue
		}

		in := store.Inbound{
			Remark:    fallback(r.remark, fmt.Sprintf("imported-%d", r.id)),
			Protocol:  proto,
			Port:      r.port,
			Enable:    r.enable,
			NodeID:    opt.NodeID,
			CreatedAt: opt.now(),
		}
		sec := store.InboundSecret{Listen: fallback(r.listen, "0.0.0.0")}

		st, warn := parseStream(r.stream)
		in.Transport = st.transport
		in.Security = st.security
		in.SNI = st.sni
		in.PublicKey = st.publicKey
		in.ShortID = st.shortID
		in.SpiderX = st.spiderX
		in.Fingerprint = fallback(st.fingerprint, "chrome")
		in.Path = st.path
		in.XHTTPMode = fallback(st.xhttpMode, "auto")
		in.HeaderType = fallback(st.headerType, "none")
		sec.PrivateKey = st.privateKey
		sec.Dest = st.dest
		for _, w := range warn {
			snap.Warnings = append(snap.Warnings, ref+": "+w)
		}

		// The address customers dial. The panel's own preference wins
		// unless the operator overrode it, because a wrong host is the
		// one mistake that silently breaks every imported link.
		host := opt.Host
		if host == "" {
			host = x.shareAddress(db, r.id)
		}
		in.Host = host
		if in.Host == "" {
			snap.Skipped = append(snap.Skipped, Skip{
				What: "inbound", Ref: ref,
				Reason: "no address to give clients — pass -host",
			})
			continue
		}

		var clients []store.Client
		var flows []string
		if normalised {
			clients, flows, err = x.clientsNormalised(db, r.id, proto, traffic, opt, snap, ref)
		} else {
			clients, flows, err = x.clientsFromSettings(r.settings, proto, traffic, opt, snap, ref)
		}
		if err != nil {
			return nil, err
		}

		// Flow is kept per client, exactly as the source had it, so a
		// customer running vision among sixteen who do not keeps working.
		// -flow overrides every one of them, for an operator who wants
		// the inbound uniform.
		if opt.Flow != "" {
			forced := strings.TrimSpace(opt.Flow)
			if forced == "none" {
				forced = ""
			}
			in.Flow = forced
			for i := range clients {
				clients[i].Flow = forced
			}
		} else {
			in.Flow = commonFlow(flows, snap, ref)
			for i := range clients {
				clients[i].Flow = flows[i]
			}
		}

		snap.Inbounds = append(snap.Inbounds, Imported{
			Inbound: in, Secret: sec, Clients: clients, SourceRef: ref,
		})
	}

	sortInbounds(snap.Inbounds)
	return snap, nil
}

// shareAddress reproduces how 3x-ui decides what address to advertise.
func (x *xuiSource) shareAddress(db *sql.DB, inboundID int64) string {
	if columnExists(db, "inbounds", "share_addr") {
		var strategy, addr sql.NullString
		err := db.QueryRow(`SELECT COALESCE(share_addr_strategy,''), COALESCE(share_addr,'')
			FROM inbounds WHERE id = ?`, inboundID).Scan(&strategy, &addr)
		if err == nil && addr.String != "" &&
			(strategy.String == "custom" || strategy.String == "") {
			return addr.String
		}
	}
	// Older panels keep it in the settings table.
	for _, key := range []string{"subDomain", "webDomain"} {
		var v string
		if tableExists(db, "settings") &&
			db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v) == nil &&
			v != "" {
			return v
		}
	}
	return ""
}

// trafficRow is the usage the source has recorded for one email.
type trafficRow struct {
	up, down, total, expiry int64
	enable                  bool
	known                   bool
}

func (x *xuiSource) readTraffic(db *sql.DB) (map[string]trafficRow, error) {
	out := map[string]trafficRow{}
	if !tableExists(db, "client_traffics") {
		return out, nil
	}
	rows, err := db.Query(`SELECT COALESCE(email,''), COALESCE(up,0), COALESCE(down,0),
		COALESCE(total,0), COALESCE(expiry_time,0), COALESCE(enable,1) FROM client_traffics`)
	if err != nil {
		return nil, fmt.Errorf("reading client traffic: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var email string
		var t trafficRow
		if err := rows.Scan(&email, &t.up, &t.down, &t.total, &t.expiry, &t.enable); err != nil {
			return nil, err
		}
		t.known = true
		out[email] = t
	}
	return out, rows.Err()
}

// clientsNormalised reads recent 3x-ui, where clients are real rows.
func (x *xuiSource) clientsNormalised(db *sql.DB, inboundID int64, proto string,
	traffic map[string]trafficRow, opt Options, snap *Snapshot, ref string,
) ([]store.Client, []string, error) {
	created := "0"
	if columnExists(db, "clients", "created_at") {
		created = "COALESCE(c.created_at,0)"
	}
	rows, err := db.Query(`SELECT c.email, COALESCE(c.uuid,''), COALESCE(c.password,''),
		COALESCE(c.sub_id,''), COALESCE(c.flow,''), COALESCE(ci.flow_override,''),
		COALESCE(c.limit_ip,0), COALESCE(c.total_gb,0), COALESCE(c.expiry_time,0),
		COALESCE(c.enable,1), `+created+` AS created_ms
		FROM clients c JOIN client_inbounds ci ON ci.client_id = c.id
		WHERE ci.inbound_id = ? ORDER BY c.id`, inboundID)
	if err != nil {
		return nil, nil, fmt.Errorf("reading clients: %w", err)
	}
	defer rows.Close()

	var out []store.Client
	var flows []string
	for rows.Next() {
		var email, uuid, password, subID, flow, flowOverride string
		var limitIP int
		var total, expiry, createdMS int64
		var enable bool
		if err := rows.Scan(&email, &uuid, &password, &subID, &flow, &flowOverride,
			&limitIP, &total, &expiry, &enable, &createdMS); err != nil {
			return nil, nil, err
		}
		if flowOverride != "" {
			flow = flowOverride
		}
		c, ok := x.buildClient(clientIn{
			email: email, uuid: uuid, password: password, subID: subID,
			flow: flow, limitIP: limitIP, total: total, expiry: expiry,
			enable: enable, proto: proto, createdMS: createdMS,
		}, traffic, opt, snap, ref)
		if ok {
			out = append(out, c)
			flows = append(flows, flow)
		}
	}
	return out, flows, rows.Err()
}

// settingsClient mirrors the JSON 3x-ui stores inside an inbound.
type settingsClient struct {
	ID         string          `json:"id"`
	Password   string          `json:"password"`
	Method     string          `json:"method"`
	Email      string          `json:"email"`
	Flow       string          `json:"flow"`
	LimitIP    int             `json:"limitIp"`
	TotalGB    int64           `json:"totalGB"`
	ExpiryTime int64           `json:"expiryTime"`
	CreatedAt  int64           `json:"created_at"`
	Enable     *bool           `json:"enable"`
	SubID      string          `json:"subId"`
	Raw        json.RawMessage `json:"-"`
}

func (x *xuiSource) clientsFromSettings(settings, proto string,
	traffic map[string]trafficRow, opt Options, snap *Snapshot, ref string,
) ([]store.Client, []string, error) {
	if strings.TrimSpace(settings) == "" {
		return nil, nil, nil
	}
	var doc struct {
		Clients []settingsClient `json:"clients"`
	}
	if err := json.Unmarshal([]byte(settings), &doc); err != nil {
		snap.Skipped = append(snap.Skipped, Skip{
			What: "inbound", Ref: ref,
			Reason: "the settings JSON could not be parsed: " + err.Error(),
		})
		return nil, nil, nil
	}
	var out []store.Client
	var flows []string
	for _, sc := range doc.Clients {
		enable := true
		if sc.Enable != nil {
			enable = *sc.Enable
		}
		c, ok := x.buildClient(clientIn{
			email: sc.Email, uuid: sc.ID, password: sc.Password, method: sc.Method,
			subID: sc.SubID, flow: sc.Flow, limitIP: sc.LimitIP, total: sc.TotalGB,
			expiry: sc.ExpiryTime, enable: enable, proto: proto, createdMS: sc.CreatedAt,
		}, traffic, opt, snap, ref)
		if ok {
			out = append(out, c)
			flows = append(flows, sc.Flow)
		}
	}
	return out, flows, nil
}

type clientIn struct {
	email, uuid, password, method, subID, flow, proto string
	limitIP                                           int
	total, expiry, createdMS                          int64
	enable                                            bool
}

// buildClient turns one source client into a HAMI client, folding in the
// recorded usage and keeping the subscription id when asked.
func (x *xuiSource) buildClient(in clientIn, traffic map[string]trafficRow,
	opt Options, snap *Snapshot, ref string,
) (store.Client, bool) {
	ident := in.email
	if ident == "" {
		ident = in.uuid
	}
	cref := ref + ", client " + fallback(ident, "(unnamed)")

	c := store.Client{
		Email:      in.email,
		UUID:       in.uuid,
		Password:   in.password,
		Method:     in.method,
		Enable:     in.enable,
		TotalBytes: in.total,
		IPLimit:    in.limitIP,
		CreatedAt:  opt.now(),
	}
	// Keep the date the customer actually signed up, when the source knows it.
	if in.createdMS > 0 {
		c.CreatedAt = time.UnixMilli(in.createdMS).UTC()
	}
	// Shadowsocks keeps its secret in a different column.
	if in.proto == "shadowsocks" {
		c.SSPassword = in.password
	}
	if opt.KeepSubTokens && in.subID != "" {
		c.SubToken = in.subID
	}

	// Usage and, on older panels, the authoritative quota and expiry.
	if t, ok := traffic[in.email]; ok && t.known {
		c.UpBytes, c.DownBytes = t.up, t.down
		if c.TotalBytes == 0 && t.total > 0 {
			c.TotalBytes = t.total
		}
		if in.expiry == 0 && t.expiry != 0 {
			in.expiry = t.expiry
		}
	}

	exp, notStarted := msToTime(in.expiry)
	c.ExpireAt = exp
	if notStarted {
		snap.Warnings = append(snap.Warnings, cref+
			": the source had a countdown that never started, imported with no expiry")
	}

	if err := validateClient(c, in.proto); err != nil {
		snap.Skipped = append(snap.Skipped, Skip{What: "client", Ref: cref, Reason: err.Error()})
		return store.Client{}, false
	}
	return c, true
}

func validateClient(c store.Client, proto string) error {
	switch proto {
	case "vless", "vmess":
		if c.UUID == "" {
			return fmt.Errorf("no uuid")
		}
	case "trojan":
		if c.Password == "" {
			return fmt.Errorf("no password")
		}
	case "shadowsocks":
		if c.SSPassword == "" && c.Password == "" {
			return fmt.Errorf("no password")
		}
	}
	if c.Email == "" {
		return fmt.Errorf("no name")
	}
	return nil
}

// commonFlow reports the flow shared by every client, or empty when they
// disagree — in which case the difference is surfaced rather than buried,
// since HAMI stores flow once per inbound.
func commonFlow(flows []string, snap *Snapshot, ref string) string {
	seen := map[string]int{}
	for _, f := range flows {
		seen[f]++
	}
	if len(seen) <= 1 {
		for f := range seen {
			return f
		}
		return ""
	}
	var parts []string
	for f, n := range seen {
		parts = append(parts, fmt.Sprintf("%s×%d", fallback(f, "(none)"), n))
	}
	sort.Strings(parts)
	// Majority wins, so the common case keeps working, and the operator is
	// told about the ones that differ.
	best, bestN := "", -1
	for f, n := range seen {
		if n > bestN || (n == bestN && f > best) {
			best, bestN = f, n
		}
	}
	snap.Warnings = append(snap.Warnings, fmt.Sprintf(
		"%s: clients use different flows (%s); each keeps its own, "+
			"and the inbound defaults to %q — pass -flow to make them uniform",
		ref, strings.Join(parts, ", "), fallback(best, "(none)")))
	return best
}

/* ── stream settings ─────────────────────────────────────────────────── */

type streamInfo struct {
	transport, security                 string
	sni, publicKey, privateKey, shortID string
	spiderX, fingerprint, dest          string
	path, xhttpMode, headerType         string
}

// parseStream reads the Xray stream settings blob. Anything it cannot make
// sense of becomes a warning rather than a silent default, because a
// wrongly guessed transport produces links that fail only on the customer's
// phone.
func parseStream(blob string) (streamInfo, []string) {
	st := streamInfo{transport: "tcp", security: "none"}
	var warn []string
	if strings.TrimSpace(blob) == "" {
		return st, []string{"no stream settings, assuming plain tcp"}
	}

	var doc struct {
		Network  string `json:"network"`
		Security string `json:"security"`
		Reality  struct {
			Dest        string   `json:"dest"`
			ServerNames []string `json:"serverNames"`
			PrivateKey  string   `json:"privateKey"`
			ShortIDs    []string `json:"shortIds"`
			Settings    struct {
				PublicKey   string `json:"publicKey"`
				Fingerprint string `json:"fingerprint"`
				ServerName  string `json:"serverName"`
				SpiderX     string `json:"spiderX"`
			} `json:"settings"`
		} `json:"realitySettings"`
		TLS struct {
			ServerName string `json:"serverName"`
			Settings   struct {
				Fingerprint string `json:"fingerprint"`
			} `json:"settings"`
		} `json:"tlsSettings"`
		WS struct {
			Path string `json:"path"`
			Host string `json:"host"`
		} `json:"wsSettings"`
		HTTPUpgrade struct {
			Path string `json:"path"`
			Host string `json:"host"`
		} `json:"httpupgradeSettings"`
		XHTTP struct {
			Path string `json:"path"`
			Host string `json:"host"`
			Mode string `json:"mode"`
		} `json:"xhttpSettings"`
		GRPC struct {
			ServiceName string `json:"serviceName"`
		} `json:"grpcSettings"`
		TCP struct {
			Header struct {
				Type string `json:"type"`
			} `json:"header"`
		} `json:"tcpSettings"`
	}
	if err := json.Unmarshal([]byte(blob), &doc); err != nil {
		return st, []string{"stream settings could not be parsed, assuming plain tcp"}
	}

	switch strings.ToLower(doc.Network) {
	case "", "tcp", "raw":
		st.transport = "tcp"
	case "ws", "websocket":
		st.transport = "ws"
	case "grpc":
		st.transport = "grpc"
	case "xhttp", "splithttp":
		st.transport = "xhttp"
	case "httpupgrade":
		st.transport = "httpupgrade"
	default:
		st.transport = "tcp"
		warn = append(warn, fmt.Sprintf("transport %q is not supported, imported as tcp — check this inbound", doc.Network))
	}

	switch strings.ToLower(doc.Security) {
	case "reality":
		st.security = "reality"
		st.dest = doc.Reality.Dest
		st.privateKey = doc.Reality.PrivateKey
		st.publicKey = doc.Reality.Settings.PublicKey
		st.fingerprint = doc.Reality.Settings.Fingerprint
		st.spiderX = doc.Reality.Settings.SpiderX
		if len(doc.Reality.ServerNames) > 0 {
			st.sni = doc.Reality.ServerNames[0]
		}
		if st.sni == "" {
			st.sni = doc.Reality.Settings.ServerName
		}
		if len(doc.Reality.ShortIDs) > 0 {
			st.shortID = doc.Reality.ShortIDs[0]
		}
		if len(doc.Reality.ShortIDs) > 1 {
			warn = append(warn, fmt.Sprintf("the source had %d short ids, HAMI keeps one (%s)",
				len(doc.Reality.ShortIDs), st.shortID))
		}
		if st.publicKey == "" {
			warn = append(warn, "reality without a public key — links will not build until it is set")
		}
	case "tls":
		st.security = "tls"
		st.sni = doc.TLS.ServerName
		st.fingerprint = doc.TLS.Settings.Fingerprint
	case "", "none":
		st.security = "none"
	default:
		st.security = "none"
		warn = append(warn, fmt.Sprintf("security %q is not supported, imported as none", doc.Security))
	}

	switch st.transport {
	case "ws":
		st.path = doc.WS.Path
		if st.sni == "" {
			st.sni = doc.WS.Host
		}
	case "httpupgrade":
		st.path = doc.HTTPUpgrade.Path
		if st.sni == "" {
			st.sni = doc.HTTPUpgrade.Host
		}
	case "xhttp":
		st.path = doc.XHTTP.Path
		st.xhttpMode = doc.XHTTP.Mode
		if st.sni == "" {
			st.sni = doc.XHTTP.Host
		}
	case "grpc":
		st.path = doc.GRPC.ServiceName
	case "tcp":
		st.headerType = doc.TCP.Header.Type
	}
	return st, warn
}

func supportedProtocol(p string) bool {
	switch p {
	case "vless", "vmess", "trojan", "shadowsocks":
		return true
	}
	return false
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
