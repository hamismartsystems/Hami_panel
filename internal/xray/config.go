// Package xray turns the same inbound a client link is built from into an
// Xray config, and supervises the core process that serves it.
//
// The rule is the same as package link: an inbound's config is built only
// from that inbound. Another inbound's Reality key, SNI, path or clients
// must never appear in it. Server-only secrets (private key, certificate
// path) must never appear in the link.
package xray

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

// Endpoint is one listening inbound plus the secrets the client link must
// not contain.
type Endpoint struct {
	Inbound link.Inbound
	Clients []link.Client

	Listen     string // default 0.0.0.0
	PrivateKey string // reality; never copied into the link
	Dest       string // reality dest, host:port
	ShortIDs   []string
	CertFile   string // tls
	KeyFile    string // tls
	Tag        string
}

// Spec is the JSON document accepted by `hami gen`.
type Spec struct {
	Inbounds []SpecInbound `json:"inbounds"`
}

// SpecInbound is one inbound in that document.
type SpecInbound struct {
	ID          int64        `json:"id"`
	Remark      string       `json:"remark"`
	Protocol    string       `json:"protocol"`
	Port        int          `json:"port"`
	Host        string       `json:"host"`
	Listen      string       `json:"listen"`
	Transport   string       `json:"transport"`
	Security    string       `json:"security"`
	SNI         string       `json:"sni"`
	PublicKey   string       `json:"publicKey"`
	PrivateKey  string       `json:"privateKey"`
	ShortID     string       `json:"shortId"`
	ShortIDs    []string     `json:"shortIds"`
	SpiderX     string       `json:"spiderX"`
	Fingerprint string       `json:"fingerprint"`
	Path        string       `json:"path"`
	XHTTPMode   string       `json:"xhttpMode"`
	HeaderType  string       `json:"headerType"`
	Flow        string       `json:"flow"`
	Dest        string       `json:"dest"`
	CertFile    string       `json:"certFile"`
	KeyFile     string       `json:"keyFile"`
	Clients     []SpecClient `json:"clients"`
}

// SpecClient is one subscriber on that inbound.
type SpecClient struct {
	UUID       string `json:"uuid"`
	Password   string `json:"password"`
	Email      string `json:"email"`
	Method     string `json:"method"`
	SSPassword string `json:"ssPassword"`
}

// Endpoints converts the document into the objects Build understands.
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
				Method: c.Method, SSPassword: c.SSPassword,
			})
		}
		out = append(out, Endpoint{
			Inbound: link.Inbound{
				ID: in.ID, Remark: in.Remark, Protocol: in.Protocol, Port: in.Port, Host: in.Host,
				Transport: link.Transport(in.Transport), Security: link.Security(in.Security),
				SNI: in.SNI, PublicKey: in.PublicKey, ShortID: in.ShortID, SpiderX: in.SpiderX,
				Fingerprint: in.Fingerprint, Path: in.Path, XHTTPMode: in.XHTTPMode,
				HeaderType: in.HeaderType, Flow: in.Flow,
			},
			Clients:    clients,
			Listen:     in.Listen,
			PrivateKey: in.PrivateKey,
			Dest:       in.Dest,
			ShortIDs:   in.ShortIDs,
			CertFile:   in.CertFile,
			KeyFile:    in.KeyFile,
			Tag:        fmt.Sprintf("in-%d", i+1),
		})
	}
	return out, nil
}

// Links builds one share link per client, each from its own inbound only.
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
			if ep.PrivateKey != "" && strings.Contains(l, ep.PrivateKey) {
				return nil, fmt.Errorf("inbound %d: private key leaked into the link", ep.Inbound.ID)
			}
			out = append(out, l)
		}
	}
	return out, nil
}

// Build returns an Xray config whose every inbound is a projection of one
// endpoint. It fails instead of emitting a config that cannot match its link.
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
		"log":       map[string]any{"loglevel": "warning"},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	// A second pass: a secret that belongs to endpoint N must not be the
	// reason endpoint M's object validates. The structural build above
	// already isolates them; this rejects a caller who put the same secret
	// on two endpoints by mistake only when we can see it is a copy of a
	// *different* endpoint's public identity. Cross-copy of a private key
	// into another endpoint's public key is the leak we actually forbid.
	if err := rejectCrossCopy(endpoints); err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func rejectCrossCopy(endpoints []Endpoint) error {
	for i, a := range endpoints {
		for j, b := range endpoints {
			if i == j {
				continue
			}
			if a.PrivateKey != "" && a.PrivateKey == b.Inbound.PublicKey {
				return fmt.Errorf("inbound %d private key is inbound %d public key", a.Inbound.ID, b.Inbound.ID)
			}
			if a.Inbound.PublicKey != "" && a.Inbound.PublicKey == b.PrivateKey {
				return fmt.Errorf("inbound %d public key is inbound %d private key", a.Inbound.ID, b.Inbound.ID)
			}
		}
	}
	return nil
}

func validate(ep Endpoint) error {
	if err := ep.Inbound.Validate(); err != nil {
		return err
	}
	if len(ep.Clients) == 0 {
		return fmt.Errorf("inbound %d: no clients", ep.Inbound.ID)
	}
	switch ep.Inbound.Security {
	case link.Reality:
		if strings.TrimSpace(ep.PrivateKey) == "" {
			return fmt.Errorf("inbound %d: reality private key is empty", ep.Inbound.ID)
		}
		if strings.TrimSpace(ep.Dest) == "" {
			return fmt.Errorf("inbound %d: reality dest is empty", ep.Inbound.ID)
		}
		ids := ep.shortIDs()
		if ep.Inbound.ShortID != "" && !contains(ids, ep.Inbound.ShortID) {
			return fmt.Errorf("inbound %d: link short id %q is not in the server short id list", ep.Inbound.ID, ep.Inbound.ShortID)
		}
	case link.TLS:
		if ep.CertFile == "" || ep.KeyFile == "" {
			return fmt.Errorf("inbound %d: tls without certificate", ep.Inbound.ID)
		}
	}
	return nil
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
	settings, err := protocolSettings(ep)
	if err != nil {
		return nil, err
	}
	stream, err := streamSettings(ep)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"tag":            tag,
		"listen":         listen,
		"port":           ep.Inbound.Port,
		"protocol":       strings.ToLower(ep.Inbound.Protocol),
		"settings":       settings,
		"streamSettings": stream,
		"sniffing": map[string]any{
			"enabled":      true,
			"destOverride": []string{"http", "tls", "quic"},
		},
	}, nil
}

func protocolSettings(ep Endpoint) (map[string]any, error) {
	proto := strings.ToLower(ep.Inbound.Protocol)
	switch proto {
	case "vless":
		clients := make([]any, 0, len(ep.Clients))
		for _, c := range ep.Clients {
			if c.UUID == "" {
				return nil, fmt.Errorf("inbound %d: vless client without uuid", ep.Inbound.ID)
			}
			item := map[string]any{"id": c.UUID, "email": c.Email}
			if ep.Inbound.Flow != "" && ep.Inbound.Security != link.None {
				item["flow"] = ep.Inbound.Flow
			}
			clients = append(clients, item)
		}
		return map[string]any{"clients": clients, "decryption": "none"}, nil
	case "vmess":
		clients := make([]any, 0, len(ep.Clients))
		for _, c := range ep.Clients {
			if c.UUID == "" {
				return nil, fmt.Errorf("inbound %d: vmess client without uuid", ep.Inbound.ID)
			}
			clients = append(clients, map[string]any{"id": c.UUID, "email": c.Email, "alterId": 0})
		}
		return map[string]any{"clients": clients}, nil
	case "trojan":
		clients := make([]any, 0, len(ep.Clients))
		for _, c := range ep.Clients {
			pw := c.Password
			if pw == "" {
				pw = c.UUID
			}
			if pw == "" {
				return nil, fmt.Errorf("inbound %d: trojan client without password", ep.Inbound.ID)
			}
			clients = append(clients, map[string]any{"password": pw, "email": c.Email})
		}
		return map[string]any{"clients": clients}, nil
	case "shadowsocks":
		clients := make([]any, 0, len(ep.Clients))
		for _, c := range ep.Clients {
			if c.Method == "" || c.SSPassword == "" {
				return nil, fmt.Errorf("inbound %d: shadowsocks client without method/password", ep.Inbound.ID)
			}
			clients = append(clients, map[string]any{
				"method": c.Method, "password": c.SSPassword, "email": c.Email,
			})
		}
		return map[string]any{"clients": clients, "network": "tcp,udp"}, nil
	default:
		return nil, fmt.Errorf("inbound %d: unsupported protocol %q", ep.Inbound.ID, ep.Inbound.Protocol)
	}
}

func streamSettings(ep Endpoint) (map[string]any, error) {
	in := ep.Inbound
	network := string(in.Transport)
	if network == "" {
		network = "tcp"
	}
	sec := string(in.Security)
	if sec == "" {
		sec = "none"
	}
	stream := map[string]any{
		"network":  network,
		"security": sec,
	}
	switch in.Transport {
	case link.TCP, "":
		if in.HeaderType == "http" {
			stream["tcpSettings"] = map[string]any{"header": map[string]any{"type": "http"}}
		}
	case link.WebSocket:
		stream["wsSettings"] = map[string]any{
			"path":    in.Path,
			"headers": map[string]any{"Host": firstNonEmpty(in.SNI, in.Host)},
		}
	case link.HTTPUpgrade:
		stream["httpupgradeSettings"] = map[string]any{
			"path": in.Path,
			"host": firstNonEmpty(in.SNI, in.Host),
		}
	case link.XHTTP:
		stream["xhttpSettings"] = map[string]any{
			"path": in.Path,
			"mode": firstNonEmpty(in.XHTTPMode, "auto"),
			"host": firstNonEmpty(in.SNI, in.Host),
		}
	case link.GRPC:
		stream["grpcSettings"] = map[string]any{"serviceName": in.Path}
	case link.QUIC:
		stream["quicSettings"] = map[string]any{
			"security": "none",
			"header":   map[string]any{"type": "none"},
		}
	default:
		return nil, fmt.Errorf("inbound %d: unsupported transport %q", in.ID, in.Transport)
	}
	switch in.Security {
	case link.Reality:
		names := []string{in.SNI}
		stream["realitySettings"] = map[string]any{
			"show":        false,
			"dest":        ep.Dest,
			"xver":        0,
			"serverNames": names,
			"privateKey":  ep.PrivateKey,
			"shortIds":    ep.shortIDs(),
		}
	case link.TLS:
		stream["tlsSettings"] = map[string]any{
			"serverName": in.SNI,
			"certificates": []any{map[string]any{
				"certificateFile": ep.CertFile,
				"keyFile":         ep.KeyFile,
			}},
		}
	case link.None, "":
		// no security object — and no leftover reality/tls settings
	default:
		return nil, fmt.Errorf("inbound %d: unsupported security %q", in.ID, in.Security)
	}
	return stream, nil
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
