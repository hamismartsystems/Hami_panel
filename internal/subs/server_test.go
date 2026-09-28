package subs

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func testServer(t *testing.T) (*Server, *store.Store, store.Client) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	in := &store.Inbound{Remark: "Reality-443", Protocol: "vless", Port: 443, Host: "cdn.example.com",
		Transport: "tcp", Security: "reality", SNI: "www.microsoft.com", PublicKey: "PK",
		ShortID: "ab12", Fingerprint: "chrome"}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(20 * 24 * time.Hour)
	c := &store.Client{InboundID: in.ID, UUID: "uuid-1", Email: "bob@x.y", TotalBytes: 10 << 30, ExpireAt: &exp}
	if err := st.CreateClient(c); err != nil {
		t.Fatal(err)
	}
	if err := st.RotateSubToken(c.ID, "srv-token-1213"); err != nil {
		t.Fatal(err)
	}
	c.SubToken = "srv-token-1213"
	srv := &Server{Store: st, BaseURL: "https://panel.x.test"}
	return srv, st, *c
}

func doGet(t *testing.T, srv *Server, path, ua string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func TestServerUnknownToken(t *testing.T) {
	srv, _, _ := testServer(t)
	rec := doGet(t, srv, "/sub/does-not-exist", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token: %d", rec.Code)
	}
}

func TestServerSubV2ray(t *testing.T) {
	srv, _, _ := testServer(t)
	rec := doGet(t, srv, "/sub/srv-token-1213", "v2rayNG/1.8")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	decoded, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatalf("body not base64: %v", err)
	}
	if !strings.Contains(string(decoded), "vless://uuid-1@cdn.example.com:443") {
		t.Fatalf("unexpected body: %s", decoded)
	}
	ui := rec.Header().Get("Subscription-Userinfo")
	if !strings.Contains(ui, "total=10737418240") || !strings.Contains(ui, "expire=") {
		t.Fatalf("userinfo header: %q", ui)
	}
	if rec.Header().Get("Profile-Web-Page-Url") != "https://panel.x.test/sub/srv-token-1213/status" {
		t.Fatalf("profile url header: %q", rec.Header().Get("Profile-Web-Page-Url"))
	}
}

func TestServerSubClashByUA(t *testing.T) {
	srv, _, _ := testServer(t)
	rec := doGet(t, srv, "/sub/srv-token-1213", "ClashforWindows/0.20")
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/yaml") {
		t.Fatalf("content-type: %q", rec.Header().Get("Content-Type"))
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if !strings.Contains(string(body), "proxies:") {
		t.Fatalf("not yaml: %s", body)
	}
}

func TestServerSubExplicitFormatWins(t *testing.T) {
	srv, _, _ := testServer(t)
	rec := doGet(t, srv, "/sub/srv-token-1213?format=singbox", "ClashForWindows")
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("explicit format ignored: %q", rec.Header().Get("Content-Type"))
	}
}

func TestServerExpiredClientGivesZeroLinksNoCache(t *testing.T) {
	srv, st, c := testServer(t)
	past := time.Now().Add(-time.Hour)
	c.ExpireAt = &past
	if err := st.UpdateClient(&c); err != nil {
		t.Fatal(err)
	}
	rec := doGet(t, srv, "/sub/srv-token-1213", "")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("empty sub must be no-store")
	}
	body, _ := io.ReadAll(rec.Result().Body)
	if strings.Contains(string(body), "vless") {
		t.Fatalf("expired client must not serve links: %s", body)
	}
}

func TestServerStatusPage(t *testing.T) {
	srv, _, _ := testServer(t)
	rec := doGet(t, srv, "/sub/srv-token-1213/status", "")
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Result().Body)
	s := string(body)
	for _, want := range []string{"وضعیت اشتراک", "فعّال", "10.00 GB", "data:image/svg+xml;base64,"} {
		if !strings.Contains(s, want) {
			t.Fatalf("status page missing %q", want)
		}
	}
	// QR svg embedded must be the sub url
	marker := "data:image/svg+xml;base64,"
	i := strings.Index(s, marker)
	svgB64 := s[i+len(marker):]
	svgB64 = svgB64[:strings.Index(svgB64, `"`)]
	svg, err := base64.StdEncoding.DecodeString(svgB64)
	if err != nil || !strings.Contains(string(svg), "<svg") {
		t.Fatalf("embedded qr invalid: %v", err)
	}
}

func TestHumanBytes(t *testing.T) {
	if got := humanBytes(5 << 30); got != "5.00 GB" {
		t.Fatalf("gb: %q", got)
	}
	if got := humanBytes(1536); got != "1536 B" {
		t.Fatalf("small: %q", got)
	}
}
