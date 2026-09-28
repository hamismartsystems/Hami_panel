package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testNode = "fr-1"
	testKey  = "0123456789abcdef0123456789abcdef0123456789abcdef"
)

func signedReq(t *testing.T, method, target string, body []byte, at time.Time) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	Sign(req, testNode, testKey, body, at)
	return req
}

func TestSignVerifyRoundTrip(t *testing.T) {
	at := time.Date(2026, 9, 28, 21, 0, 0, 0, time.UTC)
	req := signedReq(t, http.MethodGet, "http://x/health", nil, at)
	body, err := VerifyAuth(req, testNode, testKey, at)
	if err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("body: %q", body)
	}
}

func TestVerifyRejects(t *testing.T) {
	at := time.Now()
	cases := []struct {
		name string
		mod  func(r *http.Request)
	}{
		{"wrong key", func(r *http.Request) {}},
		{"wrong node", func(r *http.Request) { r.Header.Set(HeaderNode, "other") }},
		{"tampered sig", func(r *http.Request) { r.Header.Set(HeaderSig, strings.Repeat("0", 64)) }},
		{"stale ts", func(r *http.Request) {
			old := at.Add(-10 * time.Minute)
			r.Header.Set(HeaderTs, itoa(old.Unix()))
			// re-sign with the OLD timestamp, so only the window check can fail it
			Sign(r, testNode, testKey, nil, old)
		}},
		{"future ts", func(r *http.Request) {
			fut := at.Add(10 * time.Minute)
			r.Header.Set(HeaderTs, itoa(fut.Unix()))
			Sign(r, testNode, testKey, nil, fut)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := testKey
			if tc.name == "wrong key" {
				key = strings.Repeat("f", len(testKey))
			}
			req := signedReq(t, http.MethodGet, "http://x/health", nil, at)
			tc.mod(req)
			if _, err := VerifyAuth(req, testNode, key, at); err == nil {
				t.Fatalf("%s must be rejected", tc.name)
			}
		})
	}
}

func TestSignatureCoversBody(t *testing.T) {
	at := time.Now()
	body := []byte(`{"set":"config"}`)
	req := signedReq(t, http.MethodPost, "http://x/config", body, at)
	got, err := VerifyAuth(req, testNode, testKey, at)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body mismatch: %q", got)
	}
	// same headers, different body → must fail
	req2, _ := http.NewRequest(http.MethodPost, "http://x/config", bytes.NewReader([]byte(`{"set":"EVIL"}`)))
	for k, v := range req.Header {
		req2.Header[k] = v
	}
	if _, err := VerifyAuth(req2, testNode, testKey, at); err == nil {
		t.Fatal("body tampering must break the signature")
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestHealthEndpointRoundTrip(t *testing.T) {
	now := time.Now()
	agentSrv := &Server{NodeName: testNode, APIKey: testKey, Now: func() time.Time { return now }}
	srv := httptest.NewServer(agentSrv.Handler())
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h, err := ProbeHealth(ctx, addr, testNode, testKey)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !h.OK {
		t.Fatalf("not ok: %+v", h)
	}
	if StatusOf(h, err) != "healthy" {
		t.Fatalf("status: %q", StatusOf(h, err))
	}
	// wrong key
	_, badErr := ProbeHealth(ctx, addr, testNode, strings.Repeat("0", 48))
	if badErr == nil {
		t.Fatal("wrong key must fail")
	}
	if status := StatusOf(h, badErr); status != "down" {
		t.Fatalf("wrong key → down, got %q", status)
	}
	// unstable classification
	if s := StatusOf(Health{OK: true, Load1: 7}, nil); s != "unstable" {
		t.Fatalf("high load → unstable, got %q", s)
	}
	if s := StatusOf(Health{OK: true, MemUse: 0.95}, nil); s != "unstable" {
		t.Fatalf("low mem → unstable, got %q", s)
	}
}

func TestHealthJSONShape(t *testing.T) {
	var buf bytes.Buffer
	h := Health{OK: true, Uptime: 5, Load1: 0.42, MemUse: 0.31}
	json.NewEncoder(&buf).Encode(h)
	var back Health
	if err := json.NewDecoder(&buf).Decode(&back); err != nil {
		t.Fatal(err)
	}
	if back != h {
		t.Fatalf("round-trip: %+v != %+v", back, h)
	}
}
