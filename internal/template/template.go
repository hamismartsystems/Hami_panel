package template

import "fmt"

// Template is a ready-made inbound configuration.
type Template struct {
	Name        string
	Description string
	Protocol    string
	Transport   string
	Security    string
	Fingerprint string
	Flow        string
	Path        string
	XHTTPMode   string
	HeaderType  string

	// sing-box
	ObfsType          string
	ObfsPassword      string
	Alpn              string
	CongestionControl string
}

// All returns the built-in templates.
func All() []Template {
	return []Template{
		{
			Name:        "vless-reality-tcp",
			Description: "VLESS + Reality, TCP — most compatible, works everywhere",
			Protocol:    "vless",
			Transport:   "tcp",
			Security:    "reality",
			Fingerprint: "chrome",
			Flow:        "xtls-rprx-vision",
		},
		{
			Name:        "vless-reality-xhttp",
			Description: "VLESS + Reality, XHTTP — better through CDNs and middleboxes",
			Protocol:    "vless",
			Transport:   "xhttp",
			Security:    "reality",
			Fingerprint: "chrome",
			Path:        "/",
			XHTTPMode:   "auto",
		},
		{
			Name:        "vless-reality-grpc",
			Description: "VLESS + Reality, gRPC — good for Google-fronted setups",
			Protocol:    "vless",
			Transport:   "grpc",
			Security:    "reality",
			Fingerprint: "chrome",
			Path:        "hami-grpc",
		},
		{
			Name:        "vless-reality-httpupgrade",
			Description: "VLESS + Reality, HTTPUpgrade — works where WebSocket is blocked",
			Protocol:    "vless",
			Transport:   "httpupgrade",
			Security:    "reality",
			Fingerprint: "chrome",
			Path:        "/",
		},
		{
			Name:        "vless-tls-xhttp",
			Description: "VLESS + TLS, XHTTP — when you have a real certificate",
			Protocol:    "vless",
			Transport:   "xhttp",
			Security:    "tls",
			Fingerprint: "chrome",
			Path:        "/",
			XHTTPMode:   "auto",
		},
		{
			Name:        "vless-tls-grpc",
			Description: "VLESS + TLS, gRPC — CDN-friendly with real cert",
			Protocol:    "vless",
			Transport:   "grpc",
			Security:    "tls",
			Path:        "hami-grpc",
		},
		{
			Name:        "vless-tcp-tls",
			Description: "VLESS + TLS, TCP — classic TLS on 443",
			Protocol:    "vless",
			Transport:   "tcp",
			Security:    "tls",
			Fingerprint: "chrome",
		},
		{
			Name:        "hysteria2-tls",
			Description: "Hysteria2 + TLS — QUIC-based, fast on lossy networks (sing-box)",
			Protocol:    "hysteria2",
			Transport:   "udp",
			Security:    "tls",
			Alpn:        "h3",
		},
		{
			Name:              "tuic-tls",
			Description:       "TUIC v5 + TLS — QUIC 0-RTT, multiplexed (sing-box)",
			Protocol:          "tuic",
			Transport:         "quic",
			Security:          "tls",
			Alpn:              "h3",
			CongestionControl: "bbr",
		},
		{
			Name:        "anytls-tls",
			Description: "AnyTLS + TLS — mitigates TLS-in-TLS fingerprint (sing-box)",
			Protocol:    "anytls",
			Transport:   "tcp",
			Security:    "tls",
		},
		{
			Name:        "anytls-reality",
			Description: "AnyTLS + Reality — AnyTLS over Reality (sing-box 1.12+)",
			Protocol:    "anytls",
			Transport:   "tcp",
			Security:    "reality",
			Fingerprint: "chrome",
		},
	}
}

// ByName finds a template.
func ByName(name string) (Template, error) {
	for _, t := range All() {
		if t.Name == name {
			return t, nil
		}
	}
	return Template{}, fmt.Errorf("unknown template %q", name)
}
