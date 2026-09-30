package importer

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// marzbanSource reads a Marzban database.
//
// Marzban is shaped differently from the x-ui family and the difference
// matters for a faithful import. In x-ui an inbound row carries everything
// — protocol, transport, Reality keys. In Marzban the database only knows
// inbound *tags*; the real definitions live in the Xray config file, and
// the hosts table holds per-tag display overrides such as the address and
// SNI shown to customers.
//
// So a Marzban import needs the Xray config alongside the database. Rather
// than guess a protocol and hand somebody links that fail on their phone,
// this adapter asks for the file and says exactly what it could not
// resolve without it.
type marzbanSource struct{}

func (m *marzbanSource) Kind() Kind { return KindMarzban }

func (m *marzbanSource) Detect(db *sql.DB) (bool, string) {
	if !tableExists(db, "users") || !tableExists(db, "proxies") {
		return false, ""
	}
	// Marzban's users table is nothing like x-ui's admin table.
	if !columnExists(db, "users", "used_traffic") || !columnExists(db, "proxies", "settings") {
		return false, ""
	}
	variant := "marzban"
	if tableExists(db, "hosts") {
		variant = "marzban with host overrides"
	}
	return true, variant
}

// XrayConfig is the part of a Marzban xray_config.json we need.
type XrayConfig struct {
	Inbounds []struct {
		Tag            string          `json:"tag"`
		Protocol       string          `json:"protocol"`
		Port           int             `json:"port"`
		Listen         string          `json:"listen"`
		StreamSettings json.RawMessage `json:"streamSettings"`
	} `json:"inbounds"`
}

// LoadXrayConfig reads the Marzban Xray config that carries the real
// inbound definitions.
func LoadXrayConfig(path string) (*XrayConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("xray config: %w", err)
	}
	var c XrayConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("xray config: %w", err)
	}
	if len(c.Inbounds) == 0 {
		return nil, fmt.Errorf("xray config: no inbounds in %s", path)
	}
	return &c, nil
}

func (m *marzbanSource) Read(db *sql.DB, opt Options) (*Snapshot, error) {
	snap := &Snapshot{}

	hosts, err := m.readHosts(db)
	if err != nil {
		return nil, err
	}
	tags, err := m.readTags(db)
	if err != nil {
		return nil, err
	}

	// Marzban keeps xray_config.json beside its database, so look there
	// before giving up and asking the operator for it.
	xray := opt.XrayConfig
	if xray == nil && opt.sourceDir != "" {
		if cfg, err := LoadXrayConfig(filepath.Join(opt.sourceDir, "xray_config.json")); err == nil {
			xray = cfg
			snap.Warnings = append(snap.Warnings,
				"used the xray config found next to the database for the inbound definitions")
		}
	}

	// Build one HAMI inbound per Marzban inbound tag.
	defs := map[string]store.Inbound{}
	secs := map[string]store.InboundSecret{}
	if xray != nil {
		for _, x := range xray.Inbounds {
			proto := strings.ToLower(x.Protocol)
			if !supportedProtocol(proto) {
				snap.Skipped = append(snap.Skipped, Skip{
					What: "inbound", Ref: "tag " + x.Tag,
					Reason: fmt.Sprintf("protocol %q is not something HAMI serves", proto),
				})
				continue
			}
			st, warns := parseStream(string(x.StreamSettings))
			for _, w := range warns {
				snap.Warnings = append(snap.Warnings, "tag "+x.Tag+": "+w)
			}
			in := store.Inbound{
				// The tag is the inbound's real name. A host's remark is a
				// link label template, not a name.
				Remark: x.Tag, Protocol: proto, Port: x.Port,
				Transport: st.transport, Security: st.security, SNI: st.sni,
				PublicKey: st.publicKey, ShortID: st.shortID, SpiderX: st.spiderX,
				Fingerprint: fallback(st.fingerprint, "chrome"),
				Path:        st.path, XHTTPMode: fallback(st.xhttpMode, "auto"),
				HeaderType: fallback(st.headerType, "none"),
				Enable:     true, NodeID: opt.NodeID, CreatedAt: opt.now(),
			}
			secs[x.Tag] = store.InboundSecret{
				PrivateKey: st.privateKey, Dest: st.dest,
				Listen: fallback(x.Listen, "0.0.0.0"),
			}
			defs[x.Tag] = in
		}
	}

	// Host rows override what customers are told to dial. Marzban fills
	// these in at link-generation time, so anything still holding a
	// placeholder like {SERVER_IP} is a template, not an address, and
	// copying it verbatim would hand customers links that cannot connect.
	for tag, h := range hosts {
		in, ok := defs[tag]
		if !ok {
			continue
		}
		if h.address != "" {
			addr := resolveServerIP(h.address)
			switch {
			case !hasPlaceholder(addr):
				in.Host = addr
				if addr != h.address {
					snap.Warnings = append(snap.Warnings, fmt.Sprintf(
						"tag %s: marzban stores the address as %q; filled in this "+
							"server's own address, %s", tag, h.address, addr))
				}
			default:
				snap.Warnings = append(snap.Warnings, fmt.Sprintf(
					"tag %s: marzban stores the address as the template %q, which only "+
						"marzban can fill in; set the address yourself with -host",
					tag, h.address))
			}
		}
		if h.port > 0 {
			in.Port = h.port
		}
		if h.sni != "" && !hasPlaceholder(h.sni) {
			in.SNI = h.sni
		}
		if h.path != "" && !hasPlaceholder(h.path) {
			in.Path = h.path
		}
		if h.fingerprint != "" {
			in.Fingerprint = h.fingerprint
		}
		if h.extra > 0 {
			snap.Warnings = append(snap.Warnings, fmt.Sprintf(
				"tag %s: marzban has %d addresses for this inbound, kept %q — "+
					"HAMI stores one address per inbound",
				tag, h.extra+1, in.Host))
		}
		defs[tag] = in
	}

	for tag := range defs {
		in := defs[tag]
		if opt.Host != "" {
			in.Host = opt.Host
		}
		defs[tag] = in
	}

	// Anything the database references but the config did not explain.
	if xray == nil {
		for _, tag := range tags {
			snap.Skipped = append(snap.Skipped, Skip{
				What: "inbound", Ref: "tag " + tag,
				Reason: "marzban keeps inbound definitions in xray_config.json, not the database — " +
					"re-run with -xray-config pointing at it",
			})
		}
	} else {
		for _, tag := range tags {
			if _, ok := defs[tag]; !ok {
				snap.Skipped = append(snap.Skipped, Skip{
					What: "inbound", Ref: "tag " + tag,
					Reason: "the database references this tag but the xray config does not define it",
				})
			}
		}
	}

	clientsByTag, err := m.readUsers(db, defs, opt, snap)
	if err != nil {
		return nil, err
	}

	for tag, in := range defs {
		if in.Host == "" {
			snap.Skipped = append(snap.Skipped, Skip{
				What: "inbound", Ref: "tag " + tag,
				Reason: "no address to give clients — add a host in marzban or pass -host",
			})
			continue
		}
		snap.Inbounds = append(snap.Inbounds, Imported{
			Inbound: in, Secret: secs[tag], Clients: clientsByTag[tag],
			SourceRef: "tag " + tag,
		})
	}
	sortInbounds(snap.Inbounds)
	return snap, nil
}

type marzbanHost struct {
	remark, address, sni, path, fingerprint string
	port                                    int
	extra                                   int // further hosts for the same tag
}

// hasPlaceholder reports whether a marzban field is still a template.
// Marzban substitutes {SERVER_IP}, {USERNAME} and friends when it builds a
// link, so an unsubstituted one is not a usable value.
func hasPlaceholder(v string) bool {
	return strings.ContainsRune(v, '{') && strings.ContainsRune(v, '}')
}

// resolveServerIP does what marzban itself does with {SERVER_IP}: fill in
// the address of the machine the panel runs on. Anything else stays a
// template, because the remaining placeholders are per-customer and have
// no meaning for an inbound.
func resolveServerIP(addr string) string {
	if !strings.Contains(addr, "{SERVER_IP}") {
		return addr
	}
	ip := ownAddressFn()
	if ip == "" {
		return addr
	}
	return strings.ReplaceAll(addr, "{SERVER_IP}", ip)
}

// ownAddressFn is swapped in tests so they do not depend on the address
// of whatever machine happens to run them.
var ownAddressFn = ownAddress

var (
	ownAddrOnce sync.Once
	ownAddrVal  string
)

// ownAddress finds the address this machine uses to reach the internet.
// The dial sends nothing; it only asks the kernel which route, and so
// which source address, an outbound packet would take. A private or
// loopback answer is discarded, since handing a customer a private
// address is worse than admitting we do not know.
func ownAddress() string {
	ownAddrOnce.Do(func() {
		conn, err := net.Dial("udp", "198.51.100.1:9")
		if err != nil {
			return
		}
		defer conn.Close()
		host, _, err := net.SplitHostPort(conn.LocalAddr().String())
		if err != nil {
			return
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
			return
		}
		ownAddrVal = ip.String()
	})
	return ownAddrVal
}

func (m *marzbanSource) readHosts(db *sql.DB) (map[string]marzbanHost, error) {
	out := map[string]marzbanHost{}
	if !tableExists(db, "hosts") {
		return out, nil
	}
	rows, err := db.Query(`SELECT COALESCE(inbound_tag,''), COALESCE(remark,''),
		COALESCE(address,''), COALESCE(port,0), COALESCE(sni,''), COALESCE(path,''),
		COALESCE(fingerprint,'') FROM hosts`)
	if err != nil {
		return nil, fmt.Errorf("reading marzban hosts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tag string
		var h marzbanHost
		var port sql.NullInt64
		if err := rows.Scan(&tag, &h.remark, &h.address, &port, &h.sni, &h.path,
			&h.fingerprint); err != nil {
			return nil, err
		}
		h.port = int(port.Int64)
		if prev, seen := out[tag]; seen {
			prev.extra++
			out[tag] = prev
			continue
		}
		out[tag] = h // the first host wins, matching marzban's own ordering
	}
	return out, rows.Err()
}

func (m *marzbanSource) readTags(db *sql.DB) ([]string, error) {
	if !tableExists(db, "inbounds") {
		return nil, nil
	}
	rows, err := db.Query(`SELECT COALESCE(tag,'') FROM inbounds ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("reading marzban inbounds: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		if t != "" {
			out = append(out, t)
		}
	}
	return out, rows.Err()
}

// readUsers walks marzban users and their proxies. A marzban user holds
// one proxy per protocol and is present on every inbound of that protocol
// unless it is explicitly excluded, so the same person becomes one HAMI
// client per matching inbound — which is exactly how HAMI models it.
func (m *marzbanSource) readUsers(db *sql.DB, defs map[string]store.Inbound,
	opt Options, snap *Snapshot,
) (map[string][]store.Client, error) {
	out := map[string][]store.Client{}
	if len(defs) == 0 {
		return out, nil
	}

	excluded, err := m.readExclusions(db)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`SELECT u.id, u.username, COALESCE(u.status,''),
		COALESCE(u.used_traffic,0), COALESCE(u.data_limit,0), u.expire,
		p.id, COALESCE(p.type,''), COALESCE(p.settings,'')
		FROM users u JOIN proxies p ON p.user_id = u.id ORDER BY u.id, p.id`)
	if err != nil {
		return nil, fmt.Errorf("reading marzban users: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var userID, proxyID int64
		var username, status, ptype, settings string
		var used, limit int64
		var expire sql.NullInt64
		if err := rows.Scan(&userID, &username, &status, &used, &limit, &expire,
			&proxyID, &ptype, &settings); err != nil {
			return nil, err
		}

		proto := marzbanProtocol(ptype)
		if proto == "" {
			snap.Skipped = append(snap.Skipped, Skip{
				What: "client", Ref: username,
				Reason: fmt.Sprintf("proxy type %q is not something HAMI serves", ptype),
			})
			continue
		}

		var ps struct {
			ID       string `json:"id"`
			Password string `json:"password"`
			Method   string `json:"method"`
			Flow     string `json:"flow"`
		}
		if settings != "" {
			_ = json.Unmarshal([]byte(settings), &ps)
		}

		c := store.Client{
			Email:      username,
			UUID:       ps.ID,
			Password:   ps.Password,
			Method:     ps.Method,
			Enable:     status == "active" || status == "on_hold",
			TotalBytes: limit,
			// Marzban keeps a single used_traffic figure rather than a
			// split, so it is recorded as download; the total is what
			// quota enforcement actually uses.
			DownBytes: used,
			CreatedAt: opt.now(),
		}
		if proto == "shadowsocks" {
			c.SSPassword = ps.Password
		}
		if expire.Valid && expire.Int64 > 0 {
			t, _ := msToTime(expire.Int64 * 1000) // marzban stores seconds
			c.ExpireAt = t
		}

		placed := false
		for tag, in := range defs {
			if in.Protocol != proto || excluded[exclusion{proxyID, tag}] {
				continue
			}
			cc := c
			if err := validateClient(cc, proto); err != nil {
				snap.Skipped = append(snap.Skipped, Skip{
					What: "client", Ref: username + " on " + tag, Reason: err.Error(),
				})
				continue
			}
			out[tag] = append(out[tag], cc)
			placed = true
		}
		if !placed {
			snap.Skipped = append(snap.Skipped, Skip{
				What: "client", Ref: username,
				Reason: fmt.Sprintf("no imported %s inbound for this user", proto),
			})
		}
	}
	return out, rows.Err()
}

type exclusion struct {
	proxyID int64
	tag     string
}

func (m *marzbanSource) readExclusions(db *sql.DB) (map[exclusion]bool, error) {
	out := map[exclusion]bool{}
	if !tableExists(db, "exclude_inbounds_association") {
		return out, nil
	}
	rows, err := db.Query(`SELECT proxy_id, COALESCE(inbound_tag,'')
		FROM exclude_inbounds_association`)
	if err != nil {
		return nil, fmt.Errorf("reading marzban exclusions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e exclusion
		if err := rows.Scan(&e.proxyID, &e.tag); err != nil {
			return nil, err
		}
		out[e] = true
	}
	return out, rows.Err()
}

func marzbanProtocol(t string) string {
	switch strings.ToLower(t) {
	case "vless":
		return "vless"
	case "vmess":
		return "vmess"
	case "trojan":
		return "trojan"
	case "shadowsocks":
		return "shadowsocks"
	}
	return ""
}
