package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func newTestServer(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, Handler(st)
}

func seed(t *testing.T, st *store.Store) (inboundID, clientID int64) {
	t.Helper()
	in := &store.Inbound{
		Remark: "Reality-443", Protocol: "vless", Port: 443, Host: "198.51.100.10",
		Transport: "tcp", Security: "reality", SNI: "www.samsung.com",
		PublicKey: "KnUN-ZE2aBvWzHtqkdaUYn8p5edV_I9b6MV8bF4DtCk",
		ShortID:   "028eff0a", Fingerprint: "chrome", Enable: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	c := &store.Client{
		InboundID: in.ID, Email: "ali", UUID: "19ce7e0d-6a1f-4c97-a9d0-5955c7338be5",
		Enable: true, SubToken: "tok-ali",
	}
	if err := st.CreateClient(c); err != nil {
		t.Fatalf("create client: %v", err)
	}
	return in.ID, c.ID
}

func login(t *testing.T, h http.Handler, user, pass string) *http.Cookie {
	t.Helper()
	body := `{"username":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatal("login did not set a session cookie")
	return nil
}

func do(h http.Handler, method, path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

/* ── the point of the whole layer: nothing leaks without a session ──── */

func TestProtectedEndpointsRejectAnonymous(t *testing.T) {
	st, h := newTestServer(t)
	seed(t, st)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{
		"/api/me", "/api/overview", "/api/inbounds", "/api/users",
		"/api/nodes", "/api/events", "/api/users/1/link",
	} {
		rec := do(h, "GET", p, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s anonymous: want 401, got %d", p, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "ali") {
			t.Errorf("GET %s leaked user data to an anonymous caller", p)
		}
	}
	for _, p := range []string{
		"/api/users/1/toggle", "/api/users/1/reset", "/api/inbounds/1/toggle",
	} {
		if rec := do(h, "POST", p, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s anonymous: want 401, got %d", p, rec.Code)
		}
	}
}

func TestDashboardRedirectsAnonymousToLogin(t *testing.T) {
	st, h := newTestServer(t)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	rec := do(h, "GET", "/hp-ui/", nil)
	if rec.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/hp-ui/login" {
		t.Fatalf("want redirect to the login page, got %q", loc)
	}
}

func TestLoginWithoutAnyAdminExplainsItself(t *testing.T) {
	_, h := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/login",
		strings.NewReader(`{"username":"x","password":"yyyyyyyy"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "hami admin create") {
		t.Errorf("the error should tell the operator how to fix it, got %s", rec.Body.String())
	}
}

func TestWrongPasswordIsRejectedAndDoesNotLeakUsernames(t *testing.T) {
	st, h := newTestServer(t)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"username":"hami","password":"wrong-one"}`,
		`{"username":"ghost","password":"wrong-one"}`,
	} {
		req := httptest.NewRequest("POST", "/api/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401 for %s, got %d", body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "invalid username or password") {
			t.Errorf("message must be identical for both cases, got %s", rec.Body.String())
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Error("a failed login must not set a cookie")
		}
	}
}

func TestSessionCookieIsHttpOnly(t *testing.T) {
	st, h := newTestServer(t)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "hami", "super-secret-1")
	if !c.HttpOnly {
		t.Error("session cookie must be HttpOnly so scripts cannot read it")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie should be SameSite=Lax")
	}
}

func TestSignedInFlowReturnsData(t *testing.T) {
	st, h := newTestServer(t)
	_, clientID := seed(t, st)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "hami", "super-secret-1")

	rec := do(h, "GET", "/api/overview", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("overview: %d %s", rec.Code, rec.Body.String())
	}
	var ov struct {
		Inbounds int `json:"inbounds"`
		Users    int `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if ov.Inbounds != 1 || ov.Users != 1 {
		t.Fatalf("want 1 inbound and 1 user, got %+v", ov)
	}

	rec = do(h, "GET", "/api/users", c)
	if !strings.Contains(rec.Body.String(), `"email":"ali"`) {
		t.Fatalf("users endpoint did not return the user: %s", rec.Body.String())
	}

	// the share link must come from this client's own inbound
	rec = do(h, "GET", "/api/users/1/link", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("link: %d %s", rec.Code, rec.Body.String())
	}
	var lk struct {
		Link string `json:"link"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &lk)
	if !strings.HasPrefix(lk.Link, "vless://") ||
		!strings.Contains(lk.Link, "security=reality") ||
		!strings.Contains(lk.Link, "19ce7e0d") {
		t.Fatalf("unexpected link: %q", lk.Link)
	}
	if strings.Contains(lk.Link, "privateKey") || strings.Contains(lk.Link, "pbk=&") {
		t.Fatalf("link leaks or misses key material: %q", lk.Link)
	}

	// toggling a user must actually change the stored state
	if rec := do(h, "POST", "/api/users/1/toggle", c); rec.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetClient(clientID)
	if got.Enable {
		t.Error("user should be disabled after toggle")
	}
}

func TestLogoutInvalidatesTheCookie(t *testing.T) {
	st, h := newTestServer(t)
	seed(t, st)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "hami", "super-secret-1")
	if rec := do(h, "GET", "/api/me", c); rec.Code != http.StatusOK {
		t.Fatalf("me before logout: %d", rec.Code)
	}
	if rec := do(h, "POST", "/api/logout", c); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := do(h, "GET", "/api/me", c); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old cookie still works after logout: %d", rec.Code)
	}
}

func TestBruteForceGetsThrottled(t *testing.T) {
	st, h := newTestServer(t)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	var throttled bool
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest("POST", "/api/login",
			strings.NewReader(`{"username":"hami","password":"nope-nope"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.9:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatal("repeated failures from one address were never throttled")
	}
}

func TestFailedLoginIsAudited(t *testing.T) {
	st, h := newTestServer(t)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/login",
		strings.NewReader(`{"username":"hami","password":"nope-nope"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(httptest.NewRecorder(), req)

	events, err := st.RecentEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if strings.Contains(e.Message, "failed login") {
			found = true
		}
	}
	if !found {
		t.Fatal("a failed login must leave an audit trail")
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	_, h := newTestServer(t)
	rec := do(h, "GET", "/hp-ui/login", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login page: %d", rec.Code)
	}
	for k, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: want %q, got %q", k, want, got)
		}
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Error("a content security policy should be set")
	}
}

func TestUserViewFlagsExpiryAndQuota(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	past := now.Add(-24 * time.Hour)
	future := now.Add(72 * time.Hour)

	v := userViewOf(store.Client{Email: "a", ExpireAt: &past, Enable: true}, "in", now)
	if !v.Expired {
		t.Error("a past expiry must be flagged")
	}
	v = userViewOf(store.Client{Email: "b", ExpireAt: &future, Enable: true}, "in", now)
	if v.Expired || v.DaysLeft == nil || *v.DaysLeft != 3 {
		t.Errorf("want 3 days left, got %+v", v.DaysLeft)
	}
	v = userViewOf(store.Client{
		Email: "c", Enable: true, TotalBytes: 100, UpBytes: 60, DownBytes: 40}, "in", now)
	if !v.OverQuota {
		t.Error("usage equal to the quota must count as over quota")
	}
	v = userViewOf(store.Client{Email: "d", Enable: true}, "in", now)
	if v.Expired || v.OverQuota || v.DaysLeft != nil {
		t.Errorf("an unlimited user must be clean: %+v", v)
	}
}

/* ── create / edit / delete through the API ──────────────────────────── */

func signedIn(t *testing.T) (*store.Store, http.Handler, *http.Cookie) {
	t.Helper()
	st, h := newTestServer(t)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	return st, h, login(t, h, "hami", "super-secret-1")
}

func postJSON(h http.Handler, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestCreateInboundAndUserThroughTheAPI(t *testing.T) {
	st, h, c := signedIn(t)

	rec := postJSON(h, "/api/inbounds", `{"template":"vless-reality-tcp","remark":"Reality-443",
		"host":"198.51.100.10","port":"443","sni":"www.samsung.com"}`, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("create inbound: %d %s", rec.Code, rec.Body.String())
	}
	var ib struct {
		ID        int64  `json:"id"`
		PublicKey string `json:"public_key"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ib)
	if ib.ID == 0 || ib.PublicKey == "" {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}

	// three users in one go
	rec = postJSON(h, "/api/users",
		`{"inbound_id":"1","email":"team","count":"3","quota_gb":"10","days":"30"}`, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("create users: %d %s", rec.Code, rec.Body.String())
	}
	var made struct {
		Created []struct {
			Email    string `json:"email"`
			SubToken string `json:"sub_token"`
		} `json:"created"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &made)
	if len(made.Created) != 3 {
		t.Fatalf("want 3 users, got %d", len(made.Created))
	}
	if made.Created[0].Email != "team-1" || made.Created[2].Email != "team-3" {
		t.Fatalf("batch naming is wrong: %+v", made.Created)
	}
	seen := map[string]bool{}
	for _, u := range made.Created {
		if u.SubToken == "" || seen[u.SubToken] {
			t.Fatalf("subscription tokens must exist and be unique: %+v", made.Created)
		}
		seen[u.SubToken] = true
	}
	all, _ := st.ListAllClients()
	if len(all) != 3 {
		t.Fatalf("store has %d clients, want 3", len(all))
	}
}

func TestCreateInboundRejectsBadInputWithAReadableError(t *testing.T) {
	_, h, c := signedIn(t)
	rec := postJSON(h, "/api/inbounds",
		`{"template":"vless-reality-tcp","remark":"x","host":"1.2.3.4","port":"443"}`, c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a Reality inbound with no SNI must be refused, got %d", rec.Code)
	}
	if !strings.Contains(strings.ToLower(rec.Body.String()), "sni") {
		t.Errorf("the error should mention the SNI, got %s", rec.Body.String())
	}
}

func TestDuplicatePortIsRefusedThroughTheAPI(t *testing.T) {
	_, h, c := signedIn(t)
	body := `{"template":"vless-reality-tcp","remark":"a","host":"1.2.3.4","port":"443","sni":"x.com"}`
	if rec := postJSON(h, "/api/inbounds", body, c); rec.Code != http.StatusOK {
		t.Fatalf("first create: %d %s", rec.Code, rec.Body.String())
	}
	rec := postJSON(h, "/api/inbounds",
		`{"template":"vless-reality-tcp","remark":"b","host":"1.2.3.4","port":"443","sni":"x.com"}`, c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("second create on the same port must fail, got %d", rec.Code)
	}
}

func TestUpdateUserRenewsAndReactivates(t *testing.T) {
	st, h, c := signedIn(t)
	seed(t, st)
	if err := st.SetClientEnabled(1, false); err != nil {
		t.Fatal(err)
	}

	rec := postJSON(h, "/api/users/1/update", `{"quota_gb":"25","days":"60"}`, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}
	got, _ := st.GetClient(1)
	if got.TotalBytes != 25*(1<<30) {
		t.Errorf("quota not applied: %d", got.TotalBytes)
	}
	if got.ExpireAt == nil {
		t.Fatal("expiry not applied")
	}
	if d := time.Until(*got.ExpireAt).Hours(); d < 59*24 || d > 61*24 {
		t.Errorf("expiry should be about 60 days out, got %.0f hours", d)
	}
	if !got.Enable {
		t.Error("renewing a disabled user should switch them back on")
	}
}

func TestUpdateUserCanClearTheExpiry(t *testing.T) {
	st, h, c := signedIn(t)
	seed(t, st)
	if rec := postJSON(h, "/api/users/1/update", `{"days":"30"}`, c); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if rec := postJSON(h, "/api/users/1/update", `{"unlimited_time":"true"}`, c); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	got, _ := st.GetClient(1)
	if got.ExpireAt != nil {
		t.Fatalf("expiry should be cleared, got %v", got.ExpireAt)
	}
}

func TestRotateTokenBreaksTheOldSubscriptionLink(t *testing.T) {
	st, h, c := signedIn(t)
	seed(t, st)
	old, _ := st.GetClient(1)

	rec := postJSON(h, "/api/users/1/rotate", ``, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SubToken string `json:"sub_token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.SubToken == "" || out.SubToken == old.SubToken {
		t.Fatal("a new, different token must be issued")
	}
	if _, err := st.ClientBySubToken(old.SubToken); err == nil {
		t.Fatal("the old token still resolves — a leaked link would keep working")
	}
	got, _ := st.ClientBySubToken(out.SubToken)
	if got == nil || got.ID != 1 {
		t.Fatal("the new token does not resolve to the user")
	}
}

func TestDeleteUserAndInbound(t *testing.T) {
	st, h, c := signedIn(t)
	inboundID, clientID := seed(t, st)

	if rec := do(h, "POST", "/api/users/1/delete", c); rec.Code != http.StatusOK {
		t.Fatalf("delete user: %d", rec.Code)
	}
	if got, _ := st.GetClient(clientID); got != nil {
		t.Fatal("the user is still there")
	}

	// a second user, then delete the whole inbound
	if _, err := postJSONErr(h, "/api/users",
		`{"inbound_id":"1","email":"bob","quota_gb":"1","days":"1"}`, c); err != nil {
		t.Fatal(err)
	}
	rec := do(h, "POST", "/api/inbounds/1/delete", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete inbound: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := st.GetInbound(inboundID); got != nil {
		t.Fatal("the inbound is still there")
	}
	left, _ := st.ListAllClients()
	if len(left) != 0 {
		t.Fatalf("deleting an inbound must take its users with it, %d left", len(left))
	}
}

func postJSONErr(h http.Handler, path, body string, c *http.Cookie) (*httptest.ResponseRecorder, error) {
	rec := postJSON(h, path, body, c)
	if rec.Code != http.StatusOK {
		return rec, errors.New(rec.Body.String())
	}
	return rec, nil
}

func TestQREndpointRendersInProcess(t *testing.T) {
	st, h, c := signedIn(t)
	seed(t, st)
	rec := do(h, "GET", "/api/users/1/qr", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("qr: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		SVG  string `json:"svg"`
		Link string `json:"link"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.Contains(out.SVG, "<svg") {
		t.Fatalf("expected inline svg, got %.80s", out.SVG)
	}
	if !strings.HasPrefix(out.Link, "vless://") {
		t.Fatalf("unexpected link %q", out.Link)
	}
}

func TestWriteEndpointsRejectAnonymous(t *testing.T) {
	st, h := newTestServer(t)
	seed(t, st)
	if _, err := st.CreateAdmin("hami", "super-secret-1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/api/inbounds", "/api/inbounds/1/delete", "/api/users",
		"/api/users/1/update", "/api/users/1/delete", "/api/users/1/rotate",
	} {
		if rec := postJSON(h, p, `{}`, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s anonymous: want 401, got %d", p, rec.Code)
		}
	}
	if rec := do(h, "GET", "/api/users/1/qr", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET qr anonymous: want 401, got %d", rec.Code)
	}
	// nothing was created or removed
	all, _ := st.ListAllClients()
	if len(all) != 1 {
		t.Fatalf("anonymous requests changed data: %d clients", len(all))
	}
}
