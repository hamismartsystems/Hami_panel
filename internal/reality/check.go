package reality

import (
	"crypto/tls"
	"fmt"
	"net"
	"strings"
	"time"
)

// CheckResult is the outcome of probing a Reality dest.
type CheckResult struct {
	OK       bool
	Dest     string
	SNI      string
	Detail   string
	CertCN   string
	Resolved []string
}

// CheckDest dials dest (host:port) with SNI and verifies the certificate
// is valid for that SNI. This is the same check Xray does before it will
// use a dest — a bad dest makes Reality fail closed.
func CheckDest(dest, sni string, timeout time.Duration) CheckResult {
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	if dest == "" {
		return CheckResult{OK: false, Detail: "dest is empty"}
	}
	if sni == "" {
		// if SNI is empty, use the host part of dest
		if h, _, err := net.SplitHostPort(dest); err == nil {
			sni = h
		} else {
			sni = dest
		}
	}
	// DNS
	ips, _ := net.LookupIP(strings.Split(dest, ":")[0])

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", dest, &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return CheckResult{OK: false, Dest: dest, SNI: sni, Detail: err.Error()}
	}
	defer conn.Close()

	cs := conn.ConnectionState()
	if len(cs.PeerCertificates) == 0 {
		return CheckResult{OK: false, Dest: dest, SNI: sni, Detail: "no certificate"}
	}
	cert := cs.PeerCertificates[0]
	// VerifyHostname checks CN and SANs
	if err := cert.VerifyHostname(sni); err != nil {
		return CheckResult{
			OK:     false,
			Dest:   dest,
			SNI:    sni,
			Detail: fmt.Sprintf("certificate is not for %s: %v", sni, err),
			CertCN: cert.Subject.CommonName,
		}
	}
	// collect resolved IPs for info
	var resolved []string
	for _, ip := range ips {
		resolved = append(resolved, ip.String())
	}
	return CheckResult{
		OK:       true,
		Dest:     dest,
		SNI:      sni,
		Detail:   fmt.Sprintf("TLS OK, cert CN=%s", cert.Subject.CommonName),
		CertCN:   cert.Subject.CommonName,
		Resolved: resolved,
	}
}
