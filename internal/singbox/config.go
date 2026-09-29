// Package singbox builds sing-box configs for Hysteria2, TUIC and AnyTLS.
// It is the second core of Hami Panel — Xray remains the primary core for
// VLESS/VMess/Trojan/Shadowsocks, sing-box handles the QUIC-based protocols.
//
// Design rule: same as xray — an inbound's config is built only from that
// inbound's own fields and its clients. No cross-inbound leakage.
package singbox

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

// Endpoint is one sing-box inbound plus secrets.
type Endpoint struct {
	Inbound link.Inbound
	Clients []link.Client

	Listen   string // default 0.0.0.0
	CertFile string
	KeyFile  string
	Tag      string

	// Reality for AnyTLS (optional)
	PrivateKey string
	Dest       string
	ShortIDs   []string
	ServerName string // SNI for Reality
}

// Spec is the JSON document accepted by `hami gen --core singbox`.
type Spec struct {
	Inbounds []SpecInbound `json:"inbounds"`
}

type SpecInbound struct {
	ID       int64  `json:"id"`
	Remark   string `json:"remark"`
	Protocol string `json:"protocol"` // hysteria2, tuic, anytls
	Port     int    `json:"port"`
	Host     string `json:"host"`
	Listen   string `json:"listen"`

	SNI               string   `json:"sni"`
	CertFile          string   `json:"certFile"`
	KeyFile           string   `json:"keyFile"`
	PrivateKey        string   `json:"privateKey"` // reality for anytls
	Dest              string   `json:"dest"`
	ShortID           string   `json:"shortId"`
	ShortIDs          []string `json:"shortIds"`
	ObfsType          string   `json:"obfsType"`
	ObfsPassword      string   `json:"obfsPassword"`
	Alpn              string   `json:"alpn"`
	CongestionControl string   `json:"congestionControl"`

	Clients []SpecClient `json:"clients"`
}

type SpecClient struct {
	UUID     string `json:"uuid"`
	Password string `json:"password"`
	Email    string `json:"email"`
}

func (s Spec) Endpoints() ([]Endpoint, error) {
	if len(s.Inbounds) == 0 {
		return nil, fmt.Errorf("spec has no inbounds")
	}
	out := make([]Endpoint, 0, len(s.Inbounds))
	for i, in := range s.Inbounds {
		clients := make([]link.Client, 0, len(in.Clients))
		for _, c := range in.Clients {
			clients = append(clients, link.Client{
				UUID: c.UUID, Password: c.Password, Email: c.Email,
			})
		}
		out = append(out, Endpoint{
			Inbound: link.Inbound{
				ID:                in.ID,
				Remark:            in.Remark,
				Protocol:          in.Protocol,
				Port:              in.Port,
				Host:              in.Host,
				SNI:               in.SNI,
				ObfsType:          in.ObfsType,
				ObfsPassword:      in.ObfsPassword,
				Alpn:              in.Alpn,
				CongestionControl: in.CongestionControl,
			},
			Clients:    clients,
			Listen:     in.Listen,
			CertFile:   in.CertFile,
			KeyFile:    in.KeyFile,
			PrivateKey: in.PrivateKey,
			Dest:       in.Dest,
			ShortIDs:   in.ShortIDs,
			ServerName: in.SNI,
			Tag:        fmt.Sprintf("in-%d", i+1),
		})
	}
	return out, nil
}

// Links builds share links for sing-box endpoints.
func Links(endpoints []Endpoint) ([]string, error) {
	var out []string
	for _, ep := range endpoints {
		if err := validate(ep); err != nil {
			return nil, err
		}
		for _, c := range ep.Clients {
			l, err := link.Build(ep.Inbound, c)
			if err != nil {
				return nil, fmt.Errorf("inbound %d: %w", ep.Inbound.ID, err)
			}
			out = append(out, l)
		}
	}
	return out, nil
}

// Build returns a sing-box config JSON.
func Build(endpoints []Endpoint) ([]byte, error) {
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no endpoints")
	}
	inbounds := make([]any, 0, len(endpoints))
	for i, ep := range endpoints {
		if err := validate(ep); err != nil {
			return nil, err
		}
		ib, err := oneInbound(ep, i)
		if err != nil {
			return nil, err
		}
		inbounds = append(inbounds, ib)
	}
	doc := map[string]any{
		"log": map[string]any{
			"level":     "info",
			"timestamp": true,
		},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "direct"},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func validate(ep Endpoint) error {
	if err := ep.Inbound.Validate(); err != nil {
		return err
	}
	if len(ep.Clients) == 0 {
		return fmt.Errorf("inbound %d: no clients", ep.Inbound.ID)
	}
	proto := strings.ToLower(ep.Inbound.Protocol)
	switch proto {
	case "hysteria2", "hy2", "tuic", "anytls":
	default:
		return fmt.Errorf("inbound %d: unsupported sing-box protocol %q", ep.Inbound.ID, ep.Inbound.Protocol)
	}
	// TLS is required for all three
	if ep.CertFile == "" || ep.KeyFile == "" {
		// Allow Reality for AnyTLS without cert files if private key + dest are set
		if proto == "anytls" && ep.PrivateKey != "" && ep.Dest != "" {
			// ok, Reality mode
		} else {
			return fmt.Errorf("inbound %d: tls cert/key required for %s", ep.Inbound.ID, proto)
		}
	}
	return nil
}

func oneInbound(ep Endpoint, index int) (map[string]any, error) {
	tag := ep.Tag
	if tag == "" {
		if ep.Inbound.ID > 0 {
			tag = fmt.Sprintf("in-%d", ep.Inbound.ID)
		} else {
			tag = fmt.Sprintf("in-%d", index+1)
		}
	}
	listen := ep.Listen
	if listen == "" {
		listen = "0.0.0.0"
	}
	proto := strings.ToLower(ep.Inbound.Protocol)
	switch proto {
	case "hysteria2", "hy2":
		return hysteria2Inbound(ep, tag, listen)
	case "tuic":
		return tuicInbound(ep, tag, listen)
	case "anytls":
		return anytlsInbound(ep, tag, listen)
	default:
		return nil, fmt.Errorf("unsupported %q", proto)
	}
}

func hysteria2Inbound(ep Endpoint, tag, listen string) (map[string]any, error) {
	users := make([]any, 0, len(ep.Clients))
	for _, c := range ep.Clients {
		pw := c.Password
		if pw == "" {
			pw = c.UUID
		}
		if pw == "" {
			return nil, fmt.Errorf("inbound %d: hysteria2 client without password", ep.Inbound.ID)
		}
		users = append(users, map[string]any{
			"name":     c.Email,
			"password": pw,
		})
	}
	tls := map[string]any{
		"enabled": true,
	}
	if ep.Inbound.SNI != "" {
		tls["server_name"] = ep.Inbound.SNI
	}
	if ep.CertFile != "" {
		tls["certificate_path"] = ep.CertFile
		tls["key_path"] = ep.KeyFile
	}
	// alpn h3 is default for hy2, but explicit is ok
	if ep.Inbound.Alpn != "" {
		tls["alpn"] = []string{ep.Inbound.Alpn}
	} else {
		tls["alpn"] = []string{"h3"}
	}
	ib := map[string]any{
		"type":        "hysteria2",
		"tag":         tag,
		"listen":      listen,
		"listen_port": ep.Inbound.Port,
		"users":       users,
		"tls":         tls,
	}
	if ep.Inbound.ObfsType != "" {
		ib["obfs"] = map[string]any{
			"type":     ep.Inbound.ObfsType,
			"password": ep.Inbound.ObfsPassword,
		}
	}
	return ib, nil
}

func tuicInbound(ep Endpoint, tag, listen string) (map[string]any, error) {
	users := make([]any, 0, len(ep.Clients))
	for _, c := range ep.Clients {
		if c.UUID == "" {
			return nil, fmt.Errorf("inbound %d: tuic client without uuid", ep.Inbound.ID)
		}
		pw := c.Password
		if pw == "" {
			return nil, fmt.Errorf("inbound %d: tuic client without password", ep.Inbound.ID)
		}
		users = append(users, map[string]any{
			"uuid":     c.UUID,
			"password": pw,
			"name":     c.Email,
		})
	}
	tls := map[string]any{
		"enabled": true,
		"alpn":    []string{orDefault(ep.Inbound.Alpn, "h3")},
	}
	if ep.Inbound.SNI != "" {
		tls["server_name"] = ep.Inbound.SNI
	}
	if ep.CertFile != "" {
		tls["certificate_path"] = ep.CertFile
		tls["key_path"] = ep.KeyFile
	}
	ib := map[string]any{
		"type":               "tuic",
		"tag":                tag,
		"listen":             listen,
		"listen_port":        ep.Inbound.Port,
		"users":              users,
		"congestion_control": orDefault(ep.Inbound.CongestionControl, "bbr"),
		"tls":                tls,
	}
	return ib, nil
}

func anytlsInbound(ep Endpoint, tag, listen string) (map[string]any, error) {
	users := make([]any, 0, len(ep.Clients))
	for _, c := range ep.Clients {
		pw := c.Password
		if pw == "" {
			pw = c.UUID
		}
		if pw == "" {
			return nil, fmt.Errorf("inbound %d: anytls client without password", ep.Inbound.ID)
		}
		users = append(users, map[string]any{
			"name":     c.Email,
			"password": pw,
		})
	}
	tls := map[string]any{
		"enabled": true,
	}
	if ep.Inbound.SNI != "" {
		tls["server_name"] = ep.Inbound.SNI
	}
	// Reality mode for AnyTLS
	if ep.PrivateKey != "" && ep.Dest != "" {
		// split dest host:port for handshake
		server := ep.Dest
		serverPort := 443
		if strings.Contains(server, ":") {
			parts := strings.Split(server, ":")
			if len(parts) == 2 {
				// keep host as is, try parse port
				server = parts[0]
				fmt.Sscanf(parts[1], "%d", &serverPort)
			}
		}
		tls["reality"] = map[string]any{
			"enabled": true,
			"handshake": map[string]any{
				"server":      server,
				"server_port": serverPort,
			},
			"private_key": ep.PrivateKey,
			"short_id":    ep.shortIDs(),
		}
	} else {
		if ep.CertFile != "" {
			tls["certificate_path"] = ep.CertFile
			tls["key_path"] = ep.KeyFile
		}
	}
	ib := map[string]any{
		"type":        "anytls",
		"tag":         tag,
		"listen":      listen,
		"listen_port": ep.Inbound.Port,
		"users":       users,
		"tls":         tls,
	}
	return ib, nil
}

func (ep Endpoint) shortIDs() []string {
	if len(ep.ShortIDs) > 0 {
		return ep.ShortIDs
	}
	if ep.Inbound.ShortID != "" {
		return []string{ep.Inbound.ShortID}
	}
	return []string{""}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
