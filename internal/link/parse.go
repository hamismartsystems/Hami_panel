package link

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Share is the public half of a link. It never carries a private key.
type Share struct {
	Protocol    string
	UUID        string
	Host        string
	Port        int
	Remark      string
	Transport   Transport
	Security    Security
	SNI         string
	PublicKey   string
	ShortID     string
	SpiderX     string
	Fingerprint string
	Path        string
	XHTTPMode   string
	HeaderType  string
	Flow        string
	HostHeader  string
}

// Parse reads a share link back into its public fields.
// Only vless is accepted here: that is the link the canary dials, and a
// parser that guesses missing fields would hide a broken link.
func Parse(raw string) (Share, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Share{}, fmt.Errorf("link: %w", err)
	}
	if u.Scheme != "vless" {
		return Share{}, fmt.Errorf("link: canary parses vless, got %q", u.Scheme)
	}
	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return Share{}, fmt.Errorf("link: host: %w", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return Share{}, fmt.Errorf("link: bad port %q", portStr)
	}
	q := u.Query()
	s := Share{
		Protocol:    "vless",
		UUID:        u.User.Username(),
		Host:        host,
		Port:        port,
		Remark:      u.Fragment,
		Transport:   Transport(orDefault(q.Get("type"), "tcp")),
		Security:    Security(orDefault(q.Get("security"), "none")),
		SNI:         q.Get("sni"),
		PublicKey:   q.Get("pbk"),
		ShortID:     q.Get("sid"),
		SpiderX:     q.Get("spx"),
		Fingerprint: q.Get("fp"),
		HeaderType:  q.Get("headerType"),
		Flow:        q.Get("flow"),
		HostHeader:  q.Get("host"),
	}
	switch s.Transport {
	case GRPC:
		s.Path = q.Get("serviceName")
	case XHTTP:
		s.Path = q.Get("path")
		s.XHTTPMode = q.Get("mode")
	default:
		s.Path = q.Get("path")
	}
	if s.UUID == "" {
		return Share{}, fmt.Errorf("link: uuid is empty")
	}
	return s, nil
}
