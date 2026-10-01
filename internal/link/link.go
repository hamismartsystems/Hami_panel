// Package link builds client configuration links ("vless://…", "vmess://…", …)
// and subscription documents.
//
// Design rule (this is the reason Hami Panel exists):
//
//	A link is built ONLY from the inbound a client is actually attached to.
//	No global lookups, no "first inbound with security=reality", no silent
//	merging of another inbound's TLS/Reality parameters. Every builder takes
//	the resolved inbound as an argument and every case is covered by tests.
package link

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Transport is the Xray network type.
type Transport string

const (
	TCP         Transport = "tcp"
	WebSocket   Transport = "ws"
	HTTPUpgrade Transport = "httpupgrade"
	XHTTP       Transport = "xhttp"
	GRPC        Transport = "grpc"
	QUIC        Transport = "quic"
)

// Security is the TLS layer.
type Security string

const (
	None    Security = "none"
	TLS     Security = "tls"
	Reality Security = "reality"
)

// Inbound is the *resolved* inbound of a client — the one source of truth.
type Inbound struct {
	ID int64
	// Brand overrides the name customers see; empty uses DefaultBrand.
	Brand    string
	Remark   string
	Protocol string // vless, vmess, trojan, shadowsocks, hysteria2, tuic, anytls
	Port     int
	Host     string // public host or IP for the link

	Transport Transport
	Security  Security

	// TLS
	SNI string

	// Reality
	PublicKey   string
	ShortID     string
	SpiderX     string
	Fingerprint string

	// transport specifics
	Path       string // ws / httpupgrade / xhttp / grpc serviceName
	XHTTPMode  string // auto, stream-one, stream-up, packet-up
	HeaderType string // tcp headerType (http / none)

	// per-user
	Flow string // xtls-rprx-vision

	// sing-box specific (hysteria2, tuic, anytls)
	ObfsType          string // salamander for hysteria2
	ObfsPassword      string
	Alpn              string // h3 for tuic/hysteria2
	CongestionControl string // bbr, cubic, new_reno for tuic
}

// Client is the credential of one subscriber on that inbound.
type Client struct {
	UUID       string
	Password   string // trojan
	Email      string
	Method     string // shadowsocks
	SSPassword string
	// Flow overrides the inbound's flow for this client. Real panels let
	// one customer run xtls-rprx-vision while the rest run none, and a
	// link that disagrees with the server simply fails to connect.
	Flow string
	// QuotaBytes and Timed are only used for the label the customer
	// sees; zero and false mean unlimited.
	QuotaBytes int64
	Timed      bool
}

// DefaultBrand is the name customers see on their configs. It is set
// once at start-up from a flag and read-only afterwards; the panel and
// the shop in front of it can carry different names.
var DefaultBrand = "HAMI"

// DisplayName is the label a customer's app shows for this config.
//
// It answers the customer's question, not the operator's: which service
// is this, how much is on it, and does it expire. The account name is
// deliberately absent — it is an internal identifier, and on a config
// sold through a bot it would print the buyer's own chat id back at
// them. Which customer a subscription belongs to is carried by the
// subscription's title instead.
func DisplayName(in Inbound, c Client) string {
	brand := in.Brand
	if brand == "" {
		brand = DefaultBrand
	}
	parts := []string{brand}
	if c.QuotaBytes > 0 {
		parts = append(parts, humanGB(c.QuotaBytes))
	} else {
		parts = append(parts, "نامحدود")
	}
	if c.Timed {
		parts = append(parts, "ماهانه")
	} else {
		parts = append(parts, "بدون‌انقضا")
	}
	return strings.Join(parts, "-")
}

// humanGB renders a quota the way a customer bought it: whole gigabytes
// when it divides evenly, one decimal when it does not.
func humanGB(b int64) string {
	const gb = 1 << 30
	if b%gb == 0 {
		return fmt.Sprintf("%dGB", b/gb)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(b)/float64(gb)), ".0") + "GB"
}

// FlowFor reports the flow this client actually uses on this inbound.
func FlowFor(in Inbound, c Client) string {
	if c.Flow != "" {
		return c.Flow
	}
	return in.Flow
}

// Validate makes sure the link builder is not fed a half-configured inbound.
// A broken link must fail loudly here — never reach a customer.
func (i Inbound) Validate() error {
	if i.Host == "" {
		return fmt.Errorf("inbound %d: host is empty", i.ID)
	}
	if i.Port <= 0 || i.Port > 65535 {
		return fmt.Errorf("inbound %d: invalid port %d", i.ID, i.Port)
	}
	switch strings.ToLower(i.Protocol) {
	case "vless", "vmess", "trojan", "shadowsocks", "hysteria2", "hy2", "tuic", "anytls":
	default:
		return fmt.Errorf("inbound %d: unsupported protocol %q", i.ID, i.Protocol)
	}
	// sing-box protocols use TLS and need SNI for the link
	switch strings.ToLower(i.Protocol) {
	case "hysteria2", "hy2", "tuic", "anytls":
		if i.SNI == "" && i.Host == "" {
			return fmt.Errorf("inbound %d: %s without sni/host", i.ID, i.Protocol)
		}
		return nil
	}
	if i.Security == Reality {
		if i.PublicKey == "" {
			return fmt.Errorf("inbound %d: reality public key is empty", i.ID)
		}
		if i.SNI == "" {
			return fmt.Errorf("inbound %d: reality server name is empty", i.ID)
		}
	}
	if i.Security == TLS && i.Transport == TCP && i.SNI == "" {
		return fmt.Errorf("inbound %d: tls without sni", i.ID)
	}
	if (i.Transport == WebSocket || i.Transport == HTTPUpgrade ||
		i.Transport == XHTTP || i.Transport == GRPC) && i.Path == "" {
		return fmt.Errorf("inbound %d: %s transport without path", i.ID, i.Transport)
	}
	return nil
}

// Build returns the share link for this client on this inbound.
func Build(in Inbound, c Client) (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	switch strings.ToLower(in.Protocol) {
	case "vless":
		return vless(in, c)
	case "vmess":
		return vmess(in, c)
	case "trojan":
		return trojan(in, c)
	case "shadowsocks":
		return shadowsocks(in, c)
	case "hysteria2", "hy2":
		return hysteria2(in, c)
	case "tuic":
		return tuic(in, c)
	case "anytls":
		return anytls(in, c)
	}
	return "", fmt.Errorf("unsupported protocol %q", in.Protocol)
}

func vless(in Inbound, c Client) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("vless client without uuid")
	}
	q := url.Values{}
	q.Set("type", string(in.Transport))
	q.Set("security", string(in.Security))

	switch in.Security {
	case TLS:
		q.Set("sni", in.SNI)
	case Reality:
		q.Set("sni", in.SNI)
		q.Set("pbk", in.PublicKey)
		if in.ShortID != "" {
			q.Set("sid", in.ShortID)
		}
		if in.SpiderX != "" {
			q.Set("spx", in.SpiderX)
		}
		q.Set("fp", orDefault(in.Fingerprint, "chrome"))
	}

	switch in.Transport {
	case TCP:
		if in.HeaderType == "http" {
			q.Set("headerType", "http")
		}
	case WebSocket, HTTPUpgrade:
		q.Set("path", in.Path)
		q.Set("host", in.SNI)
	case XHTTP:
		q.Set("path", in.Path)
		q.Set("mode", orDefault(in.XHTTPMode, "auto"))
		if in.SNI != "" {
			q.Set("host", in.SNI)
		}
	case GRPC:
		q.Set("serviceName", in.Path)
	}

	// flow only makes sense with a TLS/Reality layer on tcp-like transports
	if f := FlowFor(in, c); f != "" && in.Security != None {
		q.Set("flow", f)
	}

	u := url.URL{
		Scheme:   "vless",
		User:     url.User(c.UUID),
		Host:     fmt.Sprintf("%s:%d", hostport(in.Host), in.Port),
		RawQuery: sortedQuery(q),
	}
	return u.String() + "#" + url.QueryEscape(DisplayName(in, c)), nil
}

func vmess(in Inbound, c Client) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("vmess client without uuid")
	}
	m := map[string]interface{}{
		"v":    "2",
		"ps":   DisplayName(in, c),
		"add":  in.Host,
		"port": in.Port,
		"id":   c.UUID,
		"aid":  "0",
		"net":  string(in.Transport),
		"type": "none",
		"tls":  string(in.Security),
	}
	switch in.Transport {
	case WebSocket:
		m["path"] = in.Path
		m["host"] = orDefault(in.SNI, in.Host)
	case GRPC:
		m["path"] = in.Path
	case XHTTP:
		m["path"] = in.Path
		m["host"] = orDefault(in.SNI, in.Host)
		m["mode"] = orDefault(in.XHTTPMode, "auto")
	case TCP:
		if in.HeaderType == "http" {
			m["type"] = "http"
		}
	}
	if in.Security == TLS {
		m["sni"] = in.SNI
	}
	return "vmess://" + b64(jsonOf(m)), nil
}

func trojan(in Inbound, c Client) (string, error) {
	pw := c.Password
	if pw == "" {
		pw = c.UUID
	}
	if pw == "" {
		return "", fmt.Errorf("trojan client without password")
	}
	q := url.Values{}
	q.Set("security", string(in.Security))
	if in.Security == TLS {
		q.Set("sni", in.SNI)
	}
	switch in.Transport {
	case WebSocket, HTTPUpgrade:
		q.Set("type", string(in.Transport))
		q.Set("path", in.Path)
		q.Set("host", orDefault(in.SNI, in.Host))
	case XHTTP:
		q.Set("type", "xhttp")
		q.Set("path", in.Path)
		q.Set("mode", orDefault(in.XHTTPMode, "auto"))
	case GRPC:
		q.Set("type", "grpc")
		q.Set("serviceName", in.Path)
	case TCP:
		q.Set("type", "tcp")
	}
	u := url.URL{
		Scheme:   "trojan",
		User:     url.User(pw),
		Host:     fmt.Sprintf("%s:%d", hostport(in.Host), in.Port),
		RawQuery: sortedQuery(q),
	}
	return u.String() + "#" + url.QueryEscape(DisplayName(in, c)), nil
}

func shadowsocks(in Inbound, c Client) (string, error) {
	if c.Method == "" || c.SSPassword == "" {
		return "", fmt.Errorf("shadowsocks client without method/password")
	}
	user := base64.RawURLEncoding.EncodeToString([]byte(c.Method + ":" + c.SSPassword))
	u := url.URL{
		Scheme: "ss",
		User:   url.User(user),
		Host:   fmt.Sprintf("%s:%d", hostport(in.Host), in.Port),
	}
	return u.String() + "#" + url.QueryEscape(DisplayName(in, c)), nil
}

func hysteria2(in Inbound, c Client) (string, error) {
	pw := c.Password
	if pw == "" {
		pw = c.SSPassword
	}
	if pw == "" {
		pw = c.UUID
	}
	if pw == "" {
		return "", fmt.Errorf("hysteria2 client without password")
	}
	q := url.Values{}
	q.Set("insecure", "0")
	if in.SNI != "" {
		q.Set("sni", in.SNI)
	}
	if in.ObfsType != "" {
		q.Set("obfs", in.ObfsType)
		if in.ObfsPassword != "" {
			q.Set("obfs-password", in.ObfsPassword)
		}
	}
	if in.Alpn != "" {
		q.Set("alpn", in.Alpn)
	}
	u := url.URL{
		Scheme:   "hysteria2",
		User:     url.User(pw),
		Host:     fmt.Sprintf("%s:%d", hostport(in.Host), in.Port),
		RawQuery: sortedQuery(q),
	}
	return u.String() + "#" + url.QueryEscape(DisplayName(in, c)), nil
}

func tuic(in Inbound, c Client) (string, error) {
	if c.UUID == "" {
		return "", fmt.Errorf("tuic client without uuid")
	}
	if c.Password == "" && c.SSPassword == "" {
		return "", fmt.Errorf("tuic client without password")
	}
	pw := c.Password
	if pw == "" {
		pw = c.SSPassword
	}
	q := url.Values{}
	q.Set("congestion_control", orDefault(in.CongestionControl, "bbr"))
	q.Set("alpn", orDefault(in.Alpn, "h3"))
	q.Set("sni", orDefault(in.SNI, in.Host))
	q.Set("udp_relay_mode", "native")
	q.Set("allow_insecure", "0")
	u := url.URL{
		Scheme:   "tuic",
		User:     url.UserPassword(c.UUID, pw),
		Host:     fmt.Sprintf("%s:%d", hostport(in.Host), in.Port),
		RawQuery: sortedQuery(q),
	}
	return u.String() + "#" + url.QueryEscape(DisplayName(in, c)), nil
}

func anytls(in Inbound, c Client) (string, error) {
	pw := c.Password
	if pw == "" {
		pw = c.SSPassword
	}
	if pw == "" {
		pw = c.UUID
	}
	if pw == "" {
		return "", fmt.Errorf("anytls client without password")
	}
	q := url.Values{}
	if in.SNI != "" {
		q.Set("sni", in.SNI)
	}
	q.Set("insecure", "0")
	// reference format uses /? before query
	u := url.URL{
		Scheme:   "anytls",
		User:     url.User(pw),
		Host:     fmt.Sprintf("%s:%d", hostport(in.Host), in.Port),
		Path:     "/",
		RawQuery: sortedQuery(q),
	}
	return u.String() + "#" + url.QueryEscape(DisplayName(in, c)), nil
}

/* ── subscription ─────────────────────────────────────────────────────── */

// Subscription renders the plain (base64) subscription body from links that
// were all built from their own resolved inbound.
func Subscription(links []string) string {
	clean := make([]string, 0, len(links))
	for _, l := range links {
		l = strings.TrimSpace(l)
		if l != "" {
			clean = append(clean, l)
		}
	}
	return base64.StdEncoding.EncodeToString([]byte(strings.Join(clean, "\n")))
}

/* ── helpers ──────────────────────────────────────────────────────────── */

func hostport(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimSuffix(h, "/")
	// IPv6
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		return "[" + h + "]"
	}
	return h
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func sortedQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func jsonOf(m map[string]interface{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf("%q:", k))
		switch v := m[k].(type) {
		case int:
			b.WriteString(fmt.Sprintf("%d", v))
		default:
			b.WriteString(fmt.Sprintf("%q", fmt.Sprint(v)))
		}
	}
	b.WriteString("}")
	return b.String()
}
