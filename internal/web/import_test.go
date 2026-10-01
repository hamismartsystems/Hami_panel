package web

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/store"
	_ "modernc.org/sqlite"
)

// sourcePanel writes a small but realistic 3x-ui database to disk.
func sourcePanel(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "x-ui.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	stream := `{"network":"tcp","security":"reality","realitySettings":{` +
		`"dest":"www.samsung.com:443","serverNames":["www.samsung.com"],` +
		`"privateKey":"PRIV","shortIds":["5a9ca64f"],` +
		`"settings":{"publicKey":"PUB","fingerprint":"chrome","spiderX":"/"}}}`
	settings := `{"clients":[` +
		`{"id":"uuid-ali","email":"ali","limitIp":2,"totalGB":53687091200,` +
		`"expiryTime":1792273297039,"enable":true,"subId":"subali"},` +
		`{"id":"uuid-off","email":"off","totalGB":0,"expiryTime":0,"enable":false,"subId":"suboff"}]}`

	for _, q := range []string{
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text,
			tag text, share_addr_strategy text, share_addr text)`,
		`CREATE TABLE client_traffics (id integer primary key, inbound_id integer,
			enable numeric, email text, up integer, down integer, expiry_time integer,
			total integer)`,
		fmt.Sprintf(`INSERT INTO inbounds VALUES (1,'Imported-8443',1,8443,'vless','','%s','%s',
			'in-8443','custom','203.0.113.9')`,
			strings.ReplaceAll(settings, "'", "''"), strings.ReplaceAll(stream, "'", "''")),
		`INSERT INTO client_traffics VALUES (1,1,1,'ali',111,222,0,0),(2,1,0,'off',0,0,0,0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	return path
}

func uploadBody(t *testing.T, fields map[string]string, filePath string) (string, io.Reader) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if filePath != "" {
		f, err := os.Open(filePath)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		part, err := mw.CreateFormFile("db", filepath.Base(filePath))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(part, f); err != nil {
			t.Fatal(err)
		}
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func postMultipart(h http.Handler, cookie, path, ctype string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, body)
	req.Header.Set("Content-Type", ctype)
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body was not json (%d): %s", rec.Code, rec.Body.String())
	}
	return m
}

// loginAs creates an admin and returns a ready-to-send cookie header.
func loginAs(t *testing.T, st *store.Store, h http.Handler) string {
	t.Helper()
	if _, err := st.CreateAdmin("hami", "test-password-not-real"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "hami", "test-password-not-real")
	return c.Name + "=" + c.Value
}

func importTestServer(t *testing.T) (*store.Store, http.Handler, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "panel.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, (&Server{Store: st, DBPath: dbPath}).Routes(), dbPath
}

func TestImportPreviewNeedsASession(t *testing.T) {
	_, h, _ := importTestServer(t)
	ctype, body := uploadBody(t, map[string]string{}, sourcePanel(t))
	rec := postMultipart(h, "", "/api/import/preview", ctype, body)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous upload got %d, want 401", rec.Code)
	}
}

func TestImportPreviewWritesNothing(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	src := sourcePanel(t)
	before, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	ctype, body := uploadBody(t, map[string]string{"keep_subs": "1"}, src)
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	j := decodeBody(t, rec)

	if j["panel"] != "3x-ui" {
		t.Errorf("panel = %v", j["panel"])
	}
	if j["session"] == "" || j["session"] == nil {
		t.Error("no session came back, so apply has nothing to commit")
	}
	plan := j["plan"].(map[string]any)
	if plan["new_inbounds"].(float64) != 1 || plan["new_clients"].(float64) != 2 {
		t.Errorf("plan = %v", plan)
	}

	// the preview must not have created anything
	inbounds, _ := st.ListInbounds()
	if len(inbounds) != 0 {
		t.Errorf("preview created %d inbounds", len(inbounds))
	}
	after, _ := os.ReadFile(src)
	if !bytes.Equal(before, after) {
		t.Error("the uploaded panel's own file was modified")
	}
}

func TestImportApplyCreatesEverythingAndVerifies(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	ctype, body := uploadBody(t, map[string]string{"keep_subs": "1"}, sourcePanel(t))
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	prev := decodeBody(t, rec)
	session := prev["session"].(string)

	ctype2, body2 := uploadBody(t, map[string]string{
		"session": session, "keep_subs": "1", "expect_clients": "2"}, "")
	rec = postMultipart(h, cookie, "/api/import/apply", ctype2, body2)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	res := decodeBody(t, rec)
	if res["created_inbounds"].(float64) != 1 || res["created_clients"].(float64) != 2 {
		t.Fatalf("apply result = %v", res)
	}
	if res["verified"] != true {
		t.Error("the import did not verify itself")
	}
	if b, _ := res["backup"].(string); b == "" {
		t.Error("no backup path was reported")
	} else if _, err := os.Stat(b); err != nil {
		t.Errorf("the backup is not on disk: %v", err)
	}

	inbounds, _ := st.ListInbounds()
	if len(inbounds) != 1 || inbounds[0].Port != 8443 {
		t.Fatalf("inbounds = %+v", inbounds)
	}
	clients, _ := st.ListClientsOf(inbounds[0].ID)
	if len(clients) != 2 {
		t.Fatalf("clients = %d", len(clients))
	}
	for _, c := range clients {
		switch c.Email {
		case "ali":
			if c.SubToken != "subali" {
				t.Errorf("subscription id not kept: %q", c.SubToken)
			}
			if c.UpBytes != 111 || c.DownBytes != 222 {
				t.Errorf("usage not carried over: %+v", c)
			}
			if !c.Enable {
				t.Error("an enabled client came in disabled")
			}
		case "off":
			if c.Enable {
				t.Error("a client disabled in the old panel was switched on")
			}
		}
	}
}

func TestImportApplyRefusesAnExpiredPreview(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	ctype, body := uploadBody(t, map[string]string{"session": "nope"}, "")
	rec := postMultipart(h, cookie, "/api/import/apply", ctype, body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("unhelpful message: %s", rec.Body.String())
	}
}

func TestImportApplyRefusesWhenTheSourceMovedSincePreview(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	ctype, body := uploadBody(t, map[string]string{"keep_subs": "1"}, sourcePanel(t))
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	session := decodeBody(t, rec)["session"].(string)

	// the browser claims it approved a different amount of work
	ctype2, body2 := uploadBody(t, map[string]string{
		"session": session, "expect_clients": "99"}, "")
	rec = postMultipart(h, cookie, "/api/import/apply", ctype2, body2)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", rec.Code)
	}
	inbounds, _ := st.ListInbounds()
	if len(inbounds) != 0 {
		t.Error("it imported anyway")
	}
}

func TestImportApplyRefusesWhileConflictsRemain(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	// an inbound already owns port 8443
	existing := &store.Inbound{
		Remark: "mine", Protocol: "vless", Port: 8443, Host: "1.1.1.1",
		Transport: "tcp", Security: "none", Enable: true,
	}
	if err := st.CreateInbound(existing); err != nil {
		t.Fatal(err)
	}

	ctype, body := uploadBody(t, map[string]string{}, sourcePanel(t))
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	prev := decodeBody(t, rec)
	plan := prev["plan"].(map[string]any)
	if len(plan["conflicts"].([]any)) == 0 {
		t.Fatal("the port clash was not reported as a conflict")
	}

	ctype2, body2 := uploadBody(t, map[string]string{
		"session": prev["session"].(string)}, "")
	rec = postMultipart(h, cookie, "/api/import/apply", ctype2, body2)
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", rec.Code)
	}
	clients, _ := st.ListClientsOf(existing.ID)
	if len(clients) != 0 {
		t.Error("clients were poured into the existing inbound")
	}
}

func TestImportRerunFromTheBrowserImportsNothingTwice(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)
	src := sourcePanel(t)

	for i := 0; i < 2; i++ {
		ctype, body := uploadBody(t, map[string]string{"keep_subs": "1"}, src)
		rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
		prev := decodeBody(t, rec)
		ctype2, body2 := uploadBody(t, map[string]string{
			"session": prev["session"].(string), "keep_subs": "1"}, "")
		rec = postMultipart(h, cookie, "/api/import/apply", ctype2, body2)
		if rec.Code != http.StatusOK {
			t.Fatalf("run %d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	inbounds, _ := st.ListInbounds()
	if len(inbounds) != 1 {
		t.Fatalf("a second import duplicated the inbound: %d", len(inbounds))
	}
	clients, _ := st.ListClientsOf(inbounds[0].ID)
	if len(clients) != 2 {
		t.Errorf("a second import duplicated clients: %d", len(clients))
	}
}

func TestImportPreviewRejectsAnUnknownDatabase(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	junk := filepath.Join(t.TempDir(), "junk.db")
	db, _ := sql.Open("sqlite", "file:"+junk)
	_, _ = db.Exec(`CREATE TABLE nothing (a integer)`)
	db.Close()

	ctype, body := uploadBody(t, map[string]string{}, junk)
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "unrecognised") {
		t.Errorf("unhelpful message: %s", rec.Body.String())
	}
}

func TestImportPreviewNeedsSomethingToRead(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)
	ctype, body := uploadBody(t, map[string]string{}, "")
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
}

func TestImportCancelForgetsTheUpload(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)

	ctype, body := uploadBody(t, map[string]string{}, sourcePanel(t))
	rec := postMultipart(h, cookie, "/api/import/preview", ctype, body)
	session := decodeBody(t, rec)["session"].(string)

	ctype2, body2 := uploadBody(t, map[string]string{"session": session}, "")
	if rec := postMultipart(h, cookie, "/api/import/cancel", ctype2, body2); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %d", rec.Code)
	}
	ctype3, body3 := uploadBody(t, map[string]string{"session": session}, "")
	rec = postMultipart(h, cookie, "/api/import/apply", ctype3, body3)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a cancelled session still applied: %d", rec.Code)
	}
}

func TestDashboardShipsTheMigrateTab(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)
	req := httptest.NewRequest("GET", "/hp-ui/", nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard: %d", rec.Code)
	}
	for _, want := range []string{
		`data-p="migrate"`, `id="p-migrate"`, `id="mgForm"`,
		"mgPreview", "mgApply", "mg_verified",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the dashboard is missing %q", want)
		}
	}
}

func TestImportCandidatesFindsAPanelSittingNextToOurs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "panel.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := (&Server{Store: st, DBPath: dbPath}).Routes()
	cookie := loginAs(t, st, h)

	// drop a real 3x-ui database beside the panel's own file
	src := sourcePanel(t)
	blob, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	neighbour := filepath.Join(filepath.Dir(dbPath), "old-panel.db")
	if err := os.WriteFile(neighbour, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	// and something that is not a panel at all
	if err := os.WriteFile(filepath.Join(filepath.Dir(dbPath), "notes.db"),
		[]byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/import/candidates", nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("candidates: %d %s", rec.Code, rec.Body.String())
	}

	list, _ := decodeBody(t, rec)["candidates"].([]any)
	if len(list) != 1 {
		t.Fatalf("found %d candidates, want exactly the one real panel: %s",
			len(list), rec.Body.String())
	}
	c := list[0].(map[string]any)
	if c["path"] != neighbour {
		t.Errorf("path = %v", c["path"])
	}
	if c["panel"] != "3x-ui" {
		t.Errorf("panel = %v", c["panel"])
	}
	if c["clients"].(float64) != 2 || c["inbounds"].(float64) != 1 {
		t.Errorf("counts wrong: %v", c)
	}
}

func TestImportCandidatesSkipsOurOwnDatabaseAndItsBackups(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "panel.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := (&Server{Store: st, DBPath: dbPath}).Routes()
	cookie := loginAs(t, st, h)

	// a backup taken by a previous import must not be offered as a source
	blob, _ := os.ReadFile(dbPath)
	_ = os.WriteFile(dbPath+".before-import-20260101-000000", blob, 0o600)

	req := httptest.NewRequest("GET", "/api/import/candidates", nil)
	req.Header.Set("Cookie", cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "before-import") {
		t.Errorf("offered its own backup as a panel to import: %s", body)
	}
	if strings.Contains(body, filepath.Base(dbPath)+`"`) {
		t.Errorf("offered its own database: %s", body)
	}
}

func TestImportCandidatesNeedsASession(t *testing.T) {
	_, h, _ := importTestServer(t)
	req := httptest.NewRequest("GET", "/api/import/candidates", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func seedInbound(t *testing.T, st *store.Store, remark string, port int) *store.Inbound {
	t.Helper()
	in := &store.Inbound{
		Remark: remark, Protocol: "vless", Port: port, Host: "198.51.100.10",
		Transport: "tcp", Security: "reality", SNI: "www.example.com",
		PublicKey: "PUB", ShortID: "aabb", Fingerprint: "chrome", Enable: true,
	}
	if err := st.CreateInbound(in); err != nil {
		t.Fatal(err)
	}
	if err := st.SetInboundSecret(store.InboundSecret{
		InboundID: in.ID, PrivateKey: "PRIV", Dest: "www.example.com:443", Listen: "0.0.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	return in
}

// Before this existed, a beginner who typed the wrong address had to
// delete the inbound, which takes every customer on it offline.
func TestInboundCanBeEditedInsteadOfDeleted(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)
	in := seedInbound(t, st, "typo", 8443)

	ctype, body := uploadBody(t, map[string]string{
		"remark": "Reality-443", "host": "203.0.113.9", "port": "8443",
		"sni": "www.samsung.com", "dest": "www.samsung.com:443",
		"fingerprint": "firefox", "flow": "xtls-rprx-vision",
	}, "")
	rec := postMultipart(h, cookie, "/api/inbounds/"+fmt.Sprint(in.ID)+"/update", ctype, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body.String())
	}

	got, err := st.GetInbound(in.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	if got.Remark != "Reality-443" || got.Host != "203.0.113.9" ||
		got.SNI != "www.samsung.com" || got.Fingerprint != "firefox" ||
		got.Flow != "xtls-rprx-vision" {
		t.Errorf("edit did not stick: %+v", got)
	}
	sec, _ := st.GetInboundSecret(in.ID)
	if sec.Dest != "www.samsung.com:443" {
		t.Errorf("dest = %q", sec.Dest)
	}
	// the identity of the inbound is untouched
	if got.Protocol != "vless" || got.Transport != "tcp" || got.Security != "reality" {
		t.Errorf("an edit changed what kind of inbound this is: %+v", got)
	}
	if sec.PrivateKey != "PRIV" {
		t.Error("an edit disturbed the reality key")
	}
}

// Moving the address or port rewrites every customer's link, and the
// operator has to be told rather than find out from complaints.
func TestEditReportsWhenLinksChange(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)
	in := seedInbound(t, st, "in", 8443)

	ctype, body := uploadBody(t, map[string]string{"sni": "www.other.com"}, "")
	rec := postMultipart(h, cookie, "/api/inbounds/"+fmt.Sprint(in.ID)+"/update", ctype, body)
	if v, _ := decodeBody(t, rec)["links_changed"].(bool); v {
		t.Error("changing the sni was reported as rewriting every link")
	}

	ctype, body = uploadBody(t, map[string]string{"port": "9443"}, "")
	rec = postMultipart(h, cookie, "/api/inbounds/"+fmt.Sprint(in.ID)+"/update", ctype, body)
	if v, _ := decodeBody(t, rec)["links_changed"].(bool); !v {
		t.Error("moving the port was not reported as rewriting every link")
	}
}

func TestEditRefusesAPortAnotherInboundOwns(t *testing.T) {
	st, h, _ := importTestServer(t)
	cookie := loginAs(t, st, h)
	a := seedInbound(t, st, "a", 8443)
	seedInbound(t, st, "b", 9443)

	ctype, body := uploadBody(t, map[string]string{"port": "9443"}, "")
	rec := postMultipart(h, cookie, "/api/inbounds/"+fmt.Sprint(a.ID)+"/update", ctype, body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", rec.Code)
	}
	got, _ := st.GetInbound(a.ID)
	if got.Port != 8443 {
		t.Errorf("the port moved anyway: %d", got.Port)
	}
}

// The login page and the dashboard drifted apart three times in one
// week — the theme button named the wrong theme on one, the logo took
// the wrong colour on one, the language switch left the theme name
// stale on one. Each had the same cause: two copies of the same logic.
// This fails if a copy ever comes back.
func TestBothScreensShareOneThemeAndLanguageControl(t *testing.T) {
	pages := map[string]string{"login": loginHTML, "dashboard": dashboardHTML}
	for name, page := range pages {
		if !strings.Contains(page, "HPUI.start") {
			t.Errorf("%s does not use the shared control", name)
		}
		if strings.Contains(page, uikitPlaceholder) {
			t.Errorf("%s still has the placeholder: the control was not injected", name)
		}
		// the kernel is injected once, so exactly one definition
		if n := strings.Count(page, "window.HPUI = (function"); n != 1 {
			t.Errorf("%s holds %d copies of the control, want 1", name, n)
		}
		for _, banned := range []string{
			"const THEMES =", "const THEMES=",
			"function applyTheme(t)", "function applyTheme(id){\n  const i",
		} {
			if strings.Contains(page, banned) {
				t.Errorf("%s declares its own theme logic again (%q)", name, banned)
			}
		}
	}

	// and the three themes are named once, in one place
	for _, want := range []string{`id: 'light'`, `id: 'dark'`, `id: 'vdark'`} {
		if !strings.Contains(uikitJS, want) {
			t.Errorf("the shared control is missing %s", want)
		}
	}
}
