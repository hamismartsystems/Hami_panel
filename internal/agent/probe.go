package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ProbeHealth performs ONE signed /health call against a node agent.
// address is "host:port" of the agent (http for now; wss:// URLs pass
// through unchanged for proxies that terminate TLS in front of agents).
func ProbeHealth(ctx context.Context, address, nodeName, apiKey string) (Health, error) {
	var h Health
	scheme := "http"
	target := address
	if parsed, err := url.Parse(address); err == nil && parsed.Scheme != "" {
		target = parsed.Host + parsed.Path
		scheme = parsed.Scheme
	}
	if target == "" {
		return h, fmt.Errorf("empty agent address")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		scheme+"://"+target+"/health", nil)
	if err != nil {
		return h, err
	}
	Sign(req, nodeName, apiKey, nil, time.Now())
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return h, fmt.Errorf("agent rejected signature")
	}
	if resp.StatusCode != 200 {
		return h, fmt.Errorf("agent http %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return h, err
	}
	return h, nil
}

// StatusOf translates a probe outcome to a node status string.
// store constants are used via strings here to keep agent free of the
// panel's store dependency.
func StatusOf(h Health, err error) string {
	switch {
	case err != nil:
		return "down"
	case Degraded(h):
		return "unstable"
	default:
		return "healthy"
	}
}
