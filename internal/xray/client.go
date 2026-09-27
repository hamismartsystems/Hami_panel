package xray

import (
	"encoding/json"
	"fmt"

	"github.com/hamismartsystems/hami_panel/internal/link"
)

// ClientConfig builds an Xray client from a share link and nothing else.
// dialHost replaces only the address the socket connects to (so a canary
// can dial 127.0.0.1). Port, uuid, security, public key, SNI and path come
// from the link. A private key cannot appear because the link does not have one.
func ClientConfig(raw, dialHost string, proxyPort int) ([]byte, error) {
	s, err := link.Parse(raw)
	if err != nil {
		return nil, err
	}
	if dialHost == "" {
		dialHost = s.Host
	}
	if proxyPort <= 0 || proxyPort > 65535 {
		return nil, fmt.Errorf("client: bad proxy port %d", proxyPort)
	}
	user := map[string]any{
		"id":         s.UUID,
		"encryption": "none",
	}
	if s.Flow != "" {
		user["flow"] = s.Flow
	}
	stream := map[string]any{
		"network":  string(s.Transport),
		"security": string(s.Security),
	}
	switch s.Transport {
	case link.WebSocket:
		stream["wsSettings"] = map[string]any{
			"path":    s.Path,
			"headers": map[string]any{"Host": firstNonEmpty(s.HostHeader, s.SNI)},
		}
	case link.HTTPUpgrade:
		stream["httpupgradeSettings"] = map[string]any{
			"path": s.Path,
			"host": firstNonEmpty(s.HostHeader, s.SNI),
		}
	case link.XHTTP:
		stream["xhttpSettings"] = map[string]any{
			"path": s.Path,
			"mode": firstNonEmpty(s.XHTTPMode, "auto"),
			"host": firstNonEmpty(s.HostHeader, s.SNI),
		}
	case link.GRPC:
		stream["grpcSettings"] = map[string]any{"serviceName": s.Path}
	case link.TCP:
		if s.HeaderType == "http" {
			stream["tcpSettings"] = map[string]any{"header": map[string]any{"type": "http"}}
		}
	}
	switch s.Security {
	case link.Reality:
		stream["realitySettings"] = map[string]any{
			"serverName":  s.SNI,
			"publicKey":   s.PublicKey,
			"shortId":     s.ShortID,
			"fingerprint": firstNonEmpty(s.Fingerprint, "chrome"),
			"spiderX":     s.SpiderX,
		}
	case link.TLS:
		stream["tlsSettings"] = map[string]any{
			"serverName":    s.SNI,
			"allowInsecure": true,
		}
	}
	doc := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"listen":   "127.0.0.1",
			"port":     proxyPort,
			"protocol": "http",
		}},
		"outbounds": []any{map[string]any{
			"protocol": "vless",
			"settings": map[string]any{
				"vnext": []any{map[string]any{
					"address": dialHost,
					"port":    s.Port,
					"users":   []any{user},
				}},
			},
			"streamSettings": stream,
		}},
	}
	rawJSON, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(rawJSON, '\n'), nil
}
