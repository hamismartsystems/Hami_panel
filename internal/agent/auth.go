// Package agent implements the signed HTTP protocol between the central
// panel and the lightweight node agents.
//
// Every request carries:
//
//	X-Hami-Node: <node name>
//	X-Hami-Ts:   <unix seconds>
//	X-Hami-Sig:  hex( HMAC-SHA256( api-key, ts\nMETHOD\nPATH\nSHA256(body) ) )
//
// The timestamp must be within ±60 s of the receiver's clock. The key never
// travels on the wire. Replay within the window is possible but harmless for
// idempotent reads; mutating endpoints must be idempotent too.
package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ClockSkew bounds the accepted timestamp drift.
const ClockSkew = 60 * time.Second

const (
	HeaderNode = "X-Hami-Node"
	HeaderTs   = "X-Hami-Ts"
	HeaderSig  = "X-Hami-Sig"
)

// signature is the HMAC over the canonical request, NOT its reply.
func signature(apiKey, ts, method, path string, bodyHash [32]byte) string {
	mac := hmac.New(sha256.New, []byte(apiKey))
	io.WriteString(mac, ts)
	mac.Write([]byte{0})
	io.WriteString(mac, method)
	mac.Write([]byte{0})
	io.WriteString(mac, path)
	mac.Write([]byte{0})
	mac.Write(bodyHash[:])
	return hex.EncodeToString(mac.Sum(nil))
}

func bodyDigest(r *http.Request) ([32]byte, []byte, error) {
	var zero [32]byte
	var raw []byte
	if r.Body != nil {
		b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return zero, nil, err
		}
		raw = b
		r.Body.Close()
	}
	return sha256.Sum256(raw), raw, nil
}

// Sign adds the auth headers to req (headers replace any existing ones).
func Sign(req *http.Request, nodeName, apiKey string, body []byte, now time.Time) {
	h := sha256.Sum256(body) // nil body ⇒ sha256 of empty, same as Verify
	ts := strconv.FormatInt(now.Unix(), 10)
	req.Header.Set(HeaderNode, nodeName)
	req.Header.Set(HeaderTs, ts)
	req.Header.Set(HeaderSig, signature(apiKey, ts, req.Method, req.URL.Path, h))
}

// VerifyAuth authenticates an incoming request against the agent's key.
// It reads the body; callers should re-wrap when they need it:
//
//	raw, err := agent.VerifyAuth(r, key, time.Now())
//	r.Body = io.NopCloser(bytes.NewReader(raw))
func VerifyAuth(r *http.Request, nodeName, apiKey string, now time.Time) ([]byte, error) {
	if r.Header.Get(HeaderNode) != nodeName {
		return nil, errors.New("agent: unknown node header")
	}
	ts := r.Header.Get(HeaderTs)
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return nil, errors.New("agent: bad timestamp")
	}
	age := now.Unix() - t
	if age < -int64(ClockSkew/time.Second) || age > int64(ClockSkew/time.Second) {
		return nil, errors.New("agent: timestamp outside window")
	}
	h, body, err := bodyDigest(r)
	if err != nil {
		return nil, err
	}
	got := r.Header.Get(HeaderSig)
	want := signature(apiKey, ts, r.Method, r.URL.Path, h)
	if !hmac.Equal([]byte(got), []byte(want)) {
		return nil, errors.New("agent: bad signature")
	}
	return body, nil
}
