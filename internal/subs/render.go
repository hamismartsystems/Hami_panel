package subs

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// Format is a subscription output flavour.
type Format string

const (
	FormatV2ray        Format = "v2ray"        // base64 list of share links (default)
	FormatClash        Format = "clash"        // Clash/Mihomo YAML
	FormatSingbox      Format = "singbox"      // sing-box JSON outbounds
	FormatShadowrocket Format = "shadowrocket" // plain link list + profile headers
)

// ContentTypeMap maps formats to HTTP content types.
var ContentTypeMap = map[Format]string{
	FormatV2ray:        "text/plain; charset=utf-8",
	FormatClash:        "text/yaml; charset=utf-8",
	FormatSingbox:      "application/json; charset=utf-8",
	FormatShadowrocket: "text/plain; charset=utf-8",
}

// FormatFor picks the output format: an explicit `?format=` value wins,
// then a handful of well-known User-Agent markers, else v2ray.
func FormatFor(userAgent, explicit string) Format {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case string(FormatClash), "mihomo", "yaml":
		return FormatClash
	case string(FormatSingbox), "sing-box", "json":
		return FormatSingbox
	case string(FormatV2ray), "base64":
		return FormatV2ray
	case string(FormatShadowrocket):
		return FormatShadowrocket
	}
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "shadowrocket"):
		return FormatShadowrocket
	case strings.Contains(ua, "clash"), strings.Contains(ua, "mihomo"),
		strings.Contains(ua, "stash"):
		return FormatClash
	case strings.Contains(ua, "sing-box"), strings.Contains(ua, "sfa"),
		strings.Contains(ua, "singbox"):
		return FormatSingbox
	}
	return FormatV2ray
}

// Render builds the document for a set of entries. An empty set renders an
// empty (but syntactically valid) document — clients then show nothing
// instead of a broken config.
func Render(entries []Entry, f Format) ([]byte, error) {
	switch f {
	case FormatV2ray, FormatShadowrocket:
		links, err := linksOf(entries)
		if err != nil {
			return nil, err
		}
		body := strings.Join(links, "\n")
		if f == FormatShadowrocket {
			return []byte(body), nil
		}
		return []byte(base64.StdEncoding.EncodeToString([]byte(body))), nil
	case FormatClash:
		return renderClash(entries)
	case FormatSingbox:
		return renderSingbox(entries)
	}
	return nil, fmt.Errorf("unknown format %q", f)
}

func linksOf(entries []Entry) ([]string, error) {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		l, err := LinkOf(e)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

/* ── Clash ───────────────────────────────────────────────────────────── */

func clashNode(e Entry) ([]string, error) {
	in, c := e.Inbound, e.Client
	name := orRemark(in.Remark)
	q := strconv.Quote
	lines := []string{
		"  - name: " + q(name),
		"    server: " + q(in.Host),
		fmt.Sprintf("    port: %d", in.Port),
	}
	sec := strings.ToLower(in.Security)
	tls := sec == "tls" || sec == "reality"

	add := func(line string) {
		lines = append(lines, line)
	}

	switch strings.ToLower(in.Protocol) {
	case "vless":
		add("    uuid: " + q(c.UUID))
		lines = append([]string{lines[0], "    type: vless"}, lines[1:]...)
		add("    udp: true")
		if e.Inbound.Flow != "" && tls {
			add("    flow: " + q(e.Inbound.Flow))
		}
	case "vmess":
		add("    uuid: " + q(c.UUID))
		lines = append([]string{lines[0], "    type: vmess"}, lines[1:]...)
		add("    alterId: 0")
		add("    cipher: auto")
		add("    udp: true")
	case "trojan":
		add("    password: " + q(orPassword(c)))
		lines = append([]string{lines[0], "    type: trojan"}, lines[1:]...)
		add("    udp: true")
	case "shadowsocks":
		add("    password: " + q(c.SSPassword))
		add("    cipher: " + q(c.Method))
		lines = append([]string{lines[0], "    type: ss"}, lines[1:]...)
		add("    udp: true")
	case "hysteria2", "hy2":
		add("    password: " + q(orPassword(c)))
		lines = append([]string{lines[0], "    type: hysteria2"}, lines[1:]...)
		add("    udp: true")
		if in.ObfsType != "" {
			add("    obfs: " + q(in.ObfsType))
			if in.ObfsPassword != "" {
				add("    obfs-password: " + q(in.ObfsPassword))
			}
		}
		if in.Alpn != "" {
			add("    alpn: [" + q(in.Alpn) + "]")
		}
	case "tuic":
		add("    uuid: " + q(c.UUID))
		add("    password: " + q(orPassword(c)))
		lines = append([]string{lines[0], "    type: tuic"}, lines[1:]...)
		add("    udp: true")
		add("    alpn: [" + q(orAlpn(in.Alpn)) + "]")
		add("    congestion-controller: " + q(orCC(in.CongestionControl)))
		add("    udp-relay-mode: native")
	case "anytls":
		add("    password: " + q(orPassword(c)))
		lines = append([]string{lines[0], "    type: anytls"}, lines[1:]...)
		add("    udp: true")
	default:
		return nil, fmt.Errorf("clash: unsupported protocol %q", in.Protocol)
	}

	switch strings.ToLower(in.Transport) {
	case "tcp":
		add("    network: tcp")
	case "ws", "websocket":
		add("    network: ws")
		add("    ws-opts:")
		add("      path: " + q(in.Path))
		add("      headers: {Host: " + q(orSNIHost(in)) + "}")
	case "httpupgrade":
		// mihomo ≥1.18 supports httpupgrade natively
		add("    network: httpupgrade")
		add("    httpupgrade-opts:")
		add("      path: " + q(in.Path))
		add("      headers: {Host: " + q(orSNIHost(in)) + "}")
	case "grpc":
		add("    network: grpc")
		add("    grpc-opts: {grpc-service-name: " + q(in.Path) + "}")
	case "xhttp":
		add("    network: xhttp")
		add("    xhttp-opts:")
		add("      path: " + q(in.Path))
		add("      mode: " + q(orMode(in.XHTTPMode)))
		add("      headers: {Host: " + q(orSNIHost(in)) + "}")
	}

	if tls {
		add("    tls: true")
		add("    servername: " + q(in.SNI))
		add("    skip-cert-verify: false")
		if sec == "reality" {
			add("    client-fingerprint: " + q(orFingerprint(in.Fingerprint)))
			add("    reality-opts:")
			add("      public-key: " + q(in.PublicKey))
			if in.ShortID != "" {
				add("      short-id: " + q(in.ShortID))
			}
		}
	}
	return lines, nil
}

func renderClash(entries []Entry) ([]byte, error) {
	var b strings.Builder
	b.WriteString("proxies:\n")
	names := []string{}
	if len(entries) == 0 {
		b.WriteString("[]\n")
	}
	for _, e := range entries {
		node, err := clashNode(e)
		if err != nil {
			return nil, err
		}
		for _, l := range node {
			b.WriteString(l + "\n")
		}
		names = append(names, orRemark(e.Inbound.Remark))
	}
	b.WriteString("proxy-groups:\n")
	b.WriteString("  - name: \"PROXY\"\n")
	b.WriteString("    type: select\n")
	if len(names) == 0 {
		b.WriteString("    proxies: []\n")
	} else {
		b.WriteString("    proxies:\n")
		for _, n := range names {
			b.WriteString("      - " + strconv.Quote(n) + "\n")
		}
	}
	b.WriteString("rules:\n")
	b.WriteString("  - MATCH,PROXY\n")
	return []byte(b.String()), nil
}

/* ── sing-box ────────────────────────────────────────────────────────── */

func singboxOutbound(e Entry) (map[string]interface{}, error) {
	in, c := e.Inbound, e.Client
	m := map[string]interface{}{
		"tag":         orRemark(in.Remark),
		"server":      in.Host,
		"server_port": in.Port,
	}
	switch strings.ToLower(in.Protocol) {
	case "vless":
		m["type"] = "vless"
		m["uuid"] = c.UUID
		if in.Flow != "" && in.Security != "none" {
			m["flow"] = in.Flow
		}
	case "vmess":
		m["type"] = "vmess"
		m["uuid"] = c.UUID
		m["security"] = "auto"
	case "trojan":
		m["type"] = "trojan"
		m["password"] = orPassword(c)
	case "shadowsocks":
		m["type"] = "shadowsocks"
		m["method"] = c.Method
		m["password"] = c.SSPassword
	case "hysteria2", "hy2":
		m["type"] = "hysteria2"
		m["password"] = orPassword(c)
		if in.ObfsType != "" {
			m["obfs"] = map[string]interface{}{
				"type":     in.ObfsType,
				"password": in.ObfsPassword,
			}
		}
	case "tuic":
		m["type"] = "tuic"
		m["uuid"] = c.UUID
		m["password"] = orPassword(c)
		m["congestion_control"] = orCC(in.CongestionControl)
		m["udp_relay_mode"] = "native"
	case "anytls":
		m["type"] = "anytls"
		m["password"] = orPassword(c)
	default:
		return nil, fmt.Errorf("singbox: unsupported protocol %q", in.Protocol)
	}

	// TLS for all protocols that need it
	sec := strings.ToLower(in.Security)
	isSingbox := false
	switch strings.ToLower(in.Protocol) {
	case "hysteria2", "hy2", "tuic", "anytls":
		isSingbox = true
	}
	if sec == "tls" || sec == "reality" || isSingbox {
		tls := map[string]interface{}{
			"enabled":     true,
			"server_name": in.SNI,
		}
		// sing-box protocols use h3 alpn
		if isSingbox {
			tls["alpn"] = []string{orAlpn(in.Alpn)}
		}
		if sec == "reality" {
			tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": orFingerprint(in.Fingerprint)}
			r := map[string]interface{}{"enabled": true, "public_key": in.PublicKey}
			if in.ShortID != "" {
				r["short_id"] = in.ShortID
			}
			tls["reality"] = r
		}
		m["tls"] = tls
	}

	switch strings.ToLower(in.Transport) {
	case "ws", "websocket":
		m["transport"] = map[string]interface{}{
			"type":    "ws",
			"path":    in.Path,
			"headers": map[string]interface{}{"Host": orSNIHost(in)},
		}
	case "httpupgrade":
		m["transport"] = map[string]interface{}{
			"type": "httpupgrade",
			"path": in.Path,
			"host": orSNIHost(in),
		}
	case "grpc":
		m["transport"] = map[string]interface{}{"type": "grpc", "service_name": in.Path}
	case "xhttp":
		// sing-box has no XHTTP transport (it is Xray-only)
		return nil, fmt.Errorf("singbox: xhttp not supported, skipped")
	}
	return m, nil
}

func renderSingbox(entries []Entry) ([]byte, error) {
	outbounds := []interface{}{}
	tags := []string{}
	for _, e := range entries {
		o, err := singboxOutbound(e)
		if err != nil {
			// known limitation (e.g. xhttp): skip that proxy, keep the rest
			continue
		}
		outbounds = append(outbounds, o)
		tags = append(tags, orRemark(e.Inbound.Remark))
	}
	doc := map[string]interface{}{"outbounds": outbounds}
	if len(tags) > 0 {
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "proxy",
			"outbounds": tags,
			"default":   tags[0],
		})
		doc["outbounds"] = outbounds
	}
	return json.MarshalIndent(doc, "", "  ")
}

/* ── small shared helpers ────────────────────────────────────────────── */

func orRemark(r string) string {
	if r == "" {
		return "hami"
	}
	return r
}

func orPassword(c store.Client) string {
	if c.Password != "" {
		return c.Password
	}
	return c.UUID
}

func orSNIHost(in store.Inbound) string {
	if in.SNI != "" {
		return in.SNI
	}
	return in.Host
}

func orFingerprint(fp string) string {
	if fp == "" {
		return "chrome"
	}
	return fp
}

func orMode(m string) string {
	if m == "" {
		return "auto"
	}
	return m
}

func orAlpn(a string) string {
	if a == "" {
		return "h3"
	}
	return a
}

func orCC(c string) string {
	if c == "" {
		return "bbr"
	}
	return c
}

// sortedNames is exported for tests of helper behaviour.
func sortedNames(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
