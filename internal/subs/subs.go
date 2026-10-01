// Package subs turns clients into subscription documents: per-client share
// links in several formats (v2ray base64, Clash YAML, sing-box JSON,
// Shadowrocket), usage gating, and the customer status page.
//
// The design rule of Hami Panel still applies: every link is built from the
// inbound that client is actually attached to (internal/link), never from a
// global search — and an expired or drained client produces NO links. It is
// better for a subscriber to see "out of quota" than to download configs
// that silently cannot work (their counters run on the server anyway).
package subs

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/link"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// NewToken returns a fresh subscription token: 128 bits of crypto-random,
// URL-safe, 22 chars. Unguessable is all we need.
func NewToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is not survivable
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// ActiveNow reports whether the client may use the service at time now.
// A disabled client, an expired client, or one past its quota is inactive.
func ActiveNow(c store.Client, now time.Time) bool {
	if !c.Enable {
		return false
	}
	if c.ExpireAt != nil && !now.Before(c.ExpireAt.UTC()) {
		return false
	}
	if c.TotalBytes > 0 && c.UpBytes+c.DownBytes >= c.TotalBytes {
		return false
	}
	return true
}

// WhyInactive explains, for the status page, why service stopped.
// Call only when ActiveNow is false.
func WhyInactive(c store.Client, now time.Time) string {
	if !c.Enable {
		return "disabled"
	}
	if c.ExpireAt != nil && !now.Before(c.ExpireAt.UTC()) {
		return "expired"
	}
	if c.TotalBytes > 0 && c.UpBytes+c.DownBytes >= c.TotalBytes {
		return "quota exhausted"
	}
	return "inactive"
}

// Entry is one (client, inbound) pair ready for rendering.
type Entry struct {
	Inbound store.Inbound
	Client  store.Client
}

// LinkOf builds the share link for one entry.
func LinkOf(e Entry) (string, error) {
	return link.Build(link.Inbound{
		ID: e.Inbound.ID, Remark: e.Inbound.Remark, Protocol: e.Inbound.Protocol,
		Port: e.Inbound.Port, Host: e.Inbound.Host,
		Transport: link.Transport(e.Inbound.Transport), Security: link.Security(e.Inbound.Security),
		SNI: e.Inbound.SNI, PublicKey: e.Inbound.PublicKey, ShortID: e.Inbound.ShortID,
		SpiderX: e.Inbound.SpiderX, Fingerprint: e.Inbound.Fingerprint,
		Path: e.Inbound.Path, XHTTPMode: e.Inbound.XHTTPMode,
		HeaderType: e.Inbound.HeaderType, Flow: e.Inbound.Flow,
		ObfsType: e.Inbound.ObfsType, ObfsPassword: e.Inbound.ObfsPassword,
		Alpn: e.Inbound.Alpn, CongestionControl: e.Inbound.CongestionControl,
	}, link.Client{
		UUID: e.Client.UUID, Password: e.Client.Password, Email: e.Client.Email,
		Method: e.Client.Method, SSPassword: e.Client.SSPassword,
		Flow: e.Client.Flow,
	})
}

// Entries resolves the enabled inbounds of a client. Today a client lives on
// exactly one inbound, so this returns 0 or 1 entries; the signature already
// supports multi-inbound subscriptions.
//
// Node gating (stage 3): if the inbound runs on a node whose last probe
// failed ("down"), it is dropped from the subscription — clients then keep
// whatever still works. "unknown" (never probed) and "unstable" (degraded
// but answering) are still served: a node that has never been checked must
// not silently break every customer on it.
func Entries(st *store.Store, c store.Client, now time.Time) ([]Entry, error) {
	if !ActiveNow(c, now) {
		return nil, nil
	}
	in, err := st.GetInbound(c.InboundID)
	if err != nil {
		return nil, fmt.Errorf("inbound %d: %w", c.InboundID, err)
	}
	if !in.Enable {
		return nil, nil
	}
	if in.NodeID != 0 {
		node, err := st.GetNode(in.NodeID)
		if err != nil {
			return nil, fmt.Errorf("inbound %d node %d: %w", in.ID, in.NodeID, err)
		}
		if node.Status == store.NodeDown {
			return nil, nil
		}
	}
	return []Entry{{Inbound: *in, Client: c}}, nil
}

// UserInfoHeader renders the standard `Subscription-Userinfo` value that
// clients display in their UI: upload/download/total bytes and expiry.
func UserInfoHeader(c store.Client) string {
	h := fmt.Sprintf("upload=%d; download=%d; total=%d",
		c.UpBytes, c.DownBytes, c.TotalBytes)
	if c.ExpireAt != nil {
		h += fmt.Sprintf("; expire=%d", c.ExpireAt.UTC().Unix())
	}
	return h
}
