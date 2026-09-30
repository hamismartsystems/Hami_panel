package importer

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
	_ "modernc.org/sqlite"
)

/* ── fixtures ────────────────────────────────────────────────────────── */

func newSourceDB(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture: %v\n%s", err, s)
		}
	}
	return path
}

const realityStream = `{"network":"tcp","security":"reality","realitySettings":{
	"dest":"www.samsung.com:443","serverNames":["www.samsung.com"],
	"privateKey":"PRIV","shortIds":["5a9ca64f","deadbeef"],
	"settings":{"publicKey":"PUB","fingerprint":"chrome","spiderX":"/"}}}`

// xuiNormalised builds a recent 3x-ui: real client rows plus a join table.
func xuiNormalised(t *testing.T) string {
	return newSourceDB(t,
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text,
			tag text, share_addr_strategy text, share_addr text)`,
		`CREATE TABLE clients (id integer primary key, email text, sub_id text, uuid text,
			password text, flow text, limit_ip integer, total_gb integer,
			expiry_time integer, enable numeric)`,
		`CREATE TABLE client_inbounds (client_id integer, inbound_id integer, flow_override text)`,
		`CREATE TABLE client_traffics (id integer primary key, inbound_id integer,
			enable numeric, email text, up integer, down integer, expiry_time integer,
			total integer)`,
		fmt.Sprintf(`INSERT INTO inbounds VALUES
			(1,'Reality-443',1,443,'vless','','{}','%s','inbound-443','custom','1.2.3.4')`,
			strings.ReplaceAll(realityStream, "'", "''")),
		`INSERT INTO clients VALUES
			(1,'ali','subali','uuid-ali','','xtls-rprx-vision',2,53687091200,1792273297039,1),
			(2,'reza','subreza','uuid-reza','','',0,0,0,1),
			(3,'off','suboff','uuid-off','','',0,1073741824,0,0)`,
		`INSERT INTO client_inbounds VALUES (1,1,''),(2,1,''),(3,1,'')`,
		`INSERT INTO client_traffics VALUES
			(1,1,1,'ali',100,200,1792273297039,53687091200),
			(2,1,1,'reza',5,6,0,0)`,
	)
}

// xuiSettings builds the older layout: clients live in the settings JSON.
func xuiSettings(t *testing.T) string {
	settings := `{"clients":[
		{"id":"uuid-ali","email":"ali","flow":"xtls-rprx-vision","limitIp":2,
		 "totalGB":53687091200,"expiryTime":1792273297039,"enable":true,"subId":"subali"},
		{"id":"uuid-reza","email":"reza","flow":"xtls-rprx-vision","limitIp":0,
		 "totalGB":0,"expiryTime":0,"enable":true,"subId":"subreza"}]}`
	return newSourceDB(t,
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text, tag text)`,
		`CREATE TABLE client_traffics (id integer primary key, inbound_id integer,
			enable numeric, email text, up integer, down integer, expiry_time integer, total integer)`,
		`CREATE TABLE settings (id integer primary key, key text, value text)`,
		fmt.Sprintf(`INSERT INTO inbounds VALUES (1,'Reality-443',1,443,'vless','','%s','%s','in-443')`,
			strings.ReplaceAll(settings, "'", "''"),
			strings.ReplaceAll(realityStream, "'", "''")),
		`INSERT INTO client_traffics VALUES (1,1,1,'ali',100,200,0,0),(2,1,1,'reza',0,0,0,0)`,
		`INSERT INTO settings VALUES (1,'subDomain','panel.example.com')`,
	)
}

func openTestStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func readSnapshot(t *testing.T, path string, opt Options) *Snapshot {
	t.Helper()
	src, err := Open(path, "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	snap, err := src.Read(opt)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return snap
}

/* ── the source must come out untouched ──────────────────────────────── */

func TestSourceIsNeverModified(t *testing.T) {
	path := xuiNormalised(t)
	before, err := fileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)

	st, dbPath := openTestStore(t)
	snap := readSnapshot(t, path, Options{})
	plan, err := BuildPlan(st, snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(st, dbPath, plan); err != nil {
		t.Fatal(err)
	}

	after, err := fileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("the source database was modified")
	}
	if info2, _ := os.Stat(path); info2.Size() != info.Size() {
		t.Error("the source database changed size")
	}
}

func TestSourceCopyIsReadOnly(t *testing.T) {
	src, err := Open(xuiNormalised(t), "")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.DB.Exec(`DELETE FROM clients`); err == nil {
		t.Fatal("a write to the source copy succeeded; query_only is not in force")
	}
}

func TestVerifyUntouchedCatchesAChangedSource(t *testing.T) {
	path := xuiNormalised(t)
	src, err := Open(path, "")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()

	// Somebody edits the panel while we are mid-import.
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO clients VALUES
		(9,'late','sublate','uuid-late','','',0,0,0,1)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := src.Read(Options{}); err == nil {
		t.Fatal("expected the import to refuse after the source changed")
	} else if !strings.Contains(err.Error(), "changed while it was being read") {
		t.Errorf("unhelpful error: %v", err)
	}
}

/* ── reading each layout ─────────────────────────────────────────────── */

func TestReadsNormalised3xUI(t *testing.T) {
	snap := readSnapshot(t, xuiNormalised(t), Options{KeepSubTokens: true})
	if snap.Kind != Kind3xUI {
		t.Errorf("kind = %q", snap.Kind)
	}
	if len(snap.Inbounds) != 1 {
		t.Fatalf("got %d inbounds", len(snap.Inbounds))
	}
	in := snap.Inbounds[0]
	if in.Inbound.Host != "1.2.3.4" {
		t.Errorf("host = %q, want the share address", in.Inbound.Host)
	}
	if in.Inbound.Security != "reality" || in.Inbound.SNI != "www.samsung.com" {
		t.Errorf("reality settings not read: %+v", in.Inbound)
	}
	if in.Inbound.PublicKey != "PUB" || in.Secret.PrivateKey != "PRIV" {
		t.Error("reality keys did not come across")
	}
	if in.Inbound.ShortID != "5a9ca64f" {
		t.Errorf("short id = %q", in.Inbound.ShortID)
	}
	if len(in.Clients) != 3 {
		t.Fatalf("got %d clients", len(in.Clients))
	}

	byEmail := map[string]store.Client{}
	for _, c := range in.Clients {
		byEmail[c.Email] = c
	}
	ali := byEmail["ali"]
	if ali.UUID != "uuid-ali" || ali.SubToken != "subali" {
		t.Errorf("ali identity wrong: %+v", ali)
	}
	if ali.TotalBytes != 53687091200 || ali.UpBytes != 100 || ali.DownBytes != 200 {
		t.Errorf("ali quota/usage wrong: %+v", ali)
	}
	if ali.IPLimit != 2 {
		t.Errorf("ali ip limit = %d", ali.IPLimit)
	}
	if ali.ExpireAt == nil || ali.ExpireAt.Unix() != 1792273297 {
		t.Errorf("ali expiry wrong: %v", ali.ExpireAt)
	}
	if byEmail["reza"].ExpireAt != nil {
		t.Error("reza should have no expiry")
	}
	if byEmail["off"].Enable {
		t.Error("a disabled client came in enabled")
	}
	// the source mixes flows, which must be surfaced
	if !hasWarning(snap, "disagree on flow") {
		t.Errorf("expected a warning about mixed flows, got %v", snap.Warnings)
	}
	// two short ids, only one kept
	if !hasWarning(snap, "short ids") {
		t.Errorf("expected a warning about the extra short id, got %v", snap.Warnings)
	}
}

func TestReadsSettingsJSONLayout(t *testing.T) {
	snap := readSnapshot(t, xuiSettings(t), Options{KeepSubTokens: true})
	if len(snap.Inbounds) != 1 {
		t.Fatalf("got %d inbounds", len(snap.Inbounds))
	}
	in := snap.Inbounds[0]
	if in.Inbound.Host != "panel.example.com" {
		t.Errorf("host = %q, want the panel's own domain setting", in.Inbound.Host)
	}
	if len(in.Clients) != 2 {
		t.Fatalf("got %d clients", len(in.Clients))
	}
	if in.Inbound.Flow != "xtls-rprx-vision" {
		t.Errorf("flow = %q; every client agreed on vision", in.Inbound.Flow)
	}
	for _, c := range in.Clients {
		if c.Email == "ali" && (c.UpBytes != 100 || c.DownBytes != 200) {
			t.Errorf("usage from client_traffics not merged: %+v", c)
		}
	}
}

func TestForcedFlowOverridesTheClients(t *testing.T) {
	snap := readSnapshot(t, xuiNormalised(t), Options{Flow: "xtls-rprx-vision"})
	if got := snap.Inbounds[0].Inbound.Flow; got != "xtls-rprx-vision" {
		t.Errorf("flow = %q", got)
	}
	snap = readSnapshot(t, xuiSettings(t), Options{Flow: "none"})
	if got := snap.Inbounds[0].Inbound.Flow; got != "" {
		t.Errorf(`-flow none should clear the flow, got %q`, got)
	}
}

func TestHostOverride(t *testing.T) {
	snap := readSnapshot(t, xuiNormalised(t), Options{Host: "vpn.example.com"})
	if got := snap.Inbounds[0].Inbound.Host; got != "vpn.example.com" {
		t.Errorf("host = %q", got)
	}
}

/* ── things that must be reported, not dropped silently ──────────────── */

func TestUnsupportedProtocolIsReported(t *testing.T) {
	path := newSourceDB(t,
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text, tag text)`,
		`CREATE TABLE client_traffics (id integer primary key, email text, up integer,
			down integer, total integer, expiry_time integer, enable numeric, inbound_id integer)`,
		`INSERT INTO inbounds VALUES (1,'api',1,62789,'dokodemo-door','','{}','{}','api')`,
	)
	snap := readSnapshot(t, path, Options{})
	if len(snap.Inbounds) != 0 {
		t.Error("an unsupported inbound was imported anyway")
	}
	if len(snap.Skipped) != 1 || !strings.Contains(snap.Skipped[0].Reason, "dokodemo-door") {
		t.Errorf("expected a clear skip reason, got %+v", snap.Skipped)
	}
}

func TestInboundWithoutAnAddressIsSkipped(t *testing.T) {
	path := newSourceDB(t,
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text, tag text)`,
		`CREATE TABLE client_traffics (id integer primary key, email text, up integer,
			down integer, total integer, expiry_time integer, enable numeric, inbound_id integer)`,
		`INSERT INTO inbounds VALUES (1,'nohost',1,443,'vless','','{"clients":[]}','{}','t')`,
	)
	snap := readSnapshot(t, path, Options{})
	if len(snap.Inbounds) != 0 {
		t.Error("imported an inbound with nowhere to connect to")
	}
	if len(snap.Skipped) != 1 || !strings.Contains(snap.Skipped[0].Reason, "-host") {
		t.Errorf("the skip should tell the operator what to do, got %+v", snap.Skipped)
	}
}

func TestClientWithoutIdentityIsSkipped(t *testing.T) {
	path := newSourceDB(t,
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text,
			tag text, share_addr_strategy text, share_addr text)`,
		`CREATE TABLE client_traffics (id integer primary key, email text, up integer,
			down integer, total integer, expiry_time integer, enable numeric, inbound_id integer)`,
		`INSERT INTO inbounds VALUES (1,'in',1,443,'vless','',
			'{"clients":[{"email":"ghost","id":""}]}','{}','t','custom','1.2.3.4')`,
	)
	snap := readSnapshot(t, path, Options{})
	if n := len(snap.Inbounds[0].Clients); n != 0 {
		t.Errorf("imported %d clients that cannot connect", n)
	}
	if len(snap.Skipped) != 1 || !strings.Contains(snap.Skipped[0].Reason, "uuid") {
		t.Errorf("expected a skip explaining the missing uuid, got %+v", snap.Skipped)
	}
}

func TestNotStartedCountdownBecomesNoExpiry(t *testing.T) {
	path := newSourceDB(t,
		`CREATE TABLE inbounds (id integer primary key, remark text, enable numeric,
			port integer, protocol text, listen text, settings text, stream_settings text,
			tag text, share_addr_strategy text, share_addr text)`,
		`CREATE TABLE client_traffics (id integer primary key, email text, up integer,
			down integer, total integer, expiry_time integer, enable numeric, inbound_id integer)`,
		`INSERT INTO inbounds VALUES (1,'in',1,443,'vless','',
			'{"clients":[{"email":"pending","id":"u1","expiryTime":-2592000000}]}','{}','t','custom','1.2.3.4')`,
	)
	snap := readSnapshot(t, path, Options{})
	c := snap.Inbounds[0].Clients[0]
	if c.ExpireAt != nil {
		t.Errorf("a countdown that never started must not become a date, got %v", c.ExpireAt)
	}
	if !hasWarning(snap, "countdown that never started") {
		t.Errorf("that decision must be reported, got %v", snap.Warnings)
	}
}

/* ── planning and applying ───────────────────────────────────────────── */

func TestApplyThenRerunImportsNothingTwice(t *testing.T) {
	path := xuiNormalised(t)
	st, dbPath := openTestStore(t)

	snap := readSnapshot(t, path, Options{KeepSubTokens: true})
	plan, err := BuildPlan(st, snap)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Apply(st, dbPath, plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.CreatedInbounds != 1 || res.CreatedClients != 3 {
		t.Fatalf("first run created %d inbounds, %d clients", res.CreatedInbounds, res.CreatedClients)
	}
	if !res.Verified {
		t.Error("the first run did not verify itself")
	}
	if res.BackupPath == "" {
		t.Error("no backup was taken before writing")
	}
	if _, err := os.Stat(res.BackupPath); err != nil {
		t.Errorf("the backup is not on disk: %v", err)
	}

	snap2 := readSnapshot(t, path, Options{KeepSubTokens: true})
	plan2, err := BuildPlan(st, snap2)
	if err != nil {
		t.Fatal(err)
	}
	newIn, newCl, oldIn, oldCl := plan2.Counts()
	if newIn != 0 || newCl != 0 {
		t.Errorf("a second run wants to create %d inbounds and %d clients", newIn, newCl)
	}
	if oldIn != 1 || oldCl != 3 {
		t.Errorf("second run recognised %d inbounds and %d clients as existing", oldIn, oldCl)
	}
}

func TestPortClashBecomesAConflictNotAnOverwrite(t *testing.T) {
	st, _ := openTestStore(t)
	existing := store.Inbound{
		Remark: "mine", Protocol: "vless", Port: 443, Host: "9.9.9.9",
		Transport: "tcp", Security: "none", Enable: true, CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateInbound(&existing); err != nil {
		t.Fatal(err)
	}

	snap := readSnapshot(t, xuiNormalised(t), Options{})
	plan, err := BuildPlan(st, snap)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Inbounds) != 0 {
		t.Error("planned to import over an inbound that already owns the port")
	}
	if len(plan.Conflicts) != 1 || !strings.Contains(plan.Conflicts[0], "443") {
		t.Errorf("expected a port conflict, got %v", plan.Conflicts)
	}
}

func TestApplyPreservesEverythingThatMatters(t *testing.T) {
	st, dbPath := openTestStore(t)
	snap := readSnapshot(t, xuiNormalised(t), Options{KeepSubTokens: true})
	plan, err := BuildPlan(st, snap)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(st, dbPath, plan); err != nil {
		t.Fatal(err)
	}

	inbounds, err := st.ListInbounds()
	if err != nil || len(inbounds) != 1 {
		t.Fatalf("inbounds = %d (%v)", len(inbounds), err)
	}
	sec, err := st.GetInboundSecret(inbounds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if sec.PrivateKey != "PRIV" {
		t.Errorf("the reality private key did not survive: %q", sec.PrivateKey)
	}
	if sec.Dest != "www.samsung.com:443" {
		t.Errorf("dest = %q", sec.Dest)
	}

	clients, err := st.ListClientsOf(inbounds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 3 {
		t.Fatalf("clients = %d", len(clients))
	}
	for _, c := range clients {
		switch c.Email {
		case "ali":
			if c.SubToken != "subali" {
				t.Errorf("ali's subscription id was not kept: %q", c.SubToken)
			}
			if c.UpBytes != 100 || c.DownBytes != 200 || c.TotalBytes != 53687091200 {
				t.Errorf("ali's numbers changed: %+v", c)
			}
			if c.ExpireAt == nil || c.ExpireAt.Unix() != 1792273297 {
				t.Errorf("ali's expiry changed: %v", c.ExpireAt)
			}
		case "off":
			if c.Enable {
				t.Error("a disabled client was enabled by the import")
			}
		}
	}
}

/* ── marzban ─────────────────────────────────────────────────────────── */

func marzbanDB(t *testing.T) string {
	return newSourceDB(t,
		`CREATE TABLE users (id integer primary key, username text, status text,
			used_traffic integer, data_limit integer, expire integer)`,
		`CREATE TABLE proxies (id integer primary key, user_id integer, type text, settings text)`,
		`CREATE TABLE inbounds (id integer primary key, tag text)`,
		`CREATE TABLE hosts (id integer primary key, remark text, address text, port integer,
			path text, sni text, fingerprint text, inbound_tag text)`,
		`CREATE TABLE exclude_inbounds_association (proxy_id integer, inbound_tag text)`,
		`INSERT INTO users VALUES (1,'sara','active',1234,10737418240,1792273297),
			(2,'mina','disabled',0,0,NULL)`,
		`INSERT INTO proxies VALUES (1,1,'VLESS','{"id":"uuid-sara","flow":""}'),
			(2,2,'VLESS','{"id":"uuid-mina"}')`,
		`INSERT INTO inbounds VALUES (1,'VLESS_REALITY')`,
		`INSERT INTO hosts VALUES (1,'FI node','fi.example.com',443,'','www.samsung.com','chrome','VLESS_REALITY')`,
	)
}

func TestMarzbanWithoutTheXrayConfigExplainsItself(t *testing.T) {
	MarzbanXray = nil
	snap := readSnapshot(t, marzbanDB(t), Options{})
	if snap.Kind != KindMarzban {
		t.Fatalf("kind = %q", snap.Kind)
	}
	if len(snap.Inbounds) != 0 {
		t.Error("guessed an inbound definition that is not in the database")
	}
	if len(snap.Skipped) == 0 || !strings.Contains(snap.Skipped[0].Reason, "xray-config") {
		t.Errorf("the operator must be told what to pass, got %+v", snap.Skipped)
	}
}

func TestMarzbanWithTheXrayConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "xray_config.json")
	cfg := fmt.Sprintf(`{"inbounds":[{"tag":"VLESS_REALITY","protocol":"vless",
		"port":8443,"listen":"0.0.0.0","streamSettings":%s}]}`, realityStream)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadXrayConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	MarzbanXray = loaded
	t.Cleanup(func() { MarzbanXray = nil })

	snap := readSnapshot(t, marzbanDB(t), Options{})
	if len(snap.Inbounds) != 1 {
		t.Fatalf("got %d inbounds (%+v)", len(snap.Inbounds), snap.Skipped)
	}
	in := snap.Inbounds[0]
	if in.Inbound.Host != "fi.example.com" {
		t.Errorf("host = %q; the hosts table should win", in.Inbound.Host)
	}
	if in.Inbound.Port != 443 {
		t.Errorf("port = %d; the hosts table overrides the config", in.Inbound.Port)
	}
	if in.Inbound.Security != "reality" || in.Secret.PrivateKey != "PRIV" {
		t.Errorf("reality settings not taken from the xray config: %+v", in.Inbound)
	}
	if len(in.Clients) != 2 {
		t.Fatalf("got %d clients", len(in.Clients))
	}
	for _, c := range in.Clients {
		switch c.Email {
		case "sara":
			if !c.Enable || c.TotalBytes != 10737418240 || c.DownBytes != 1234 {
				t.Errorf("sara did not come across: %+v", c)
			}
			if c.ExpireAt == nil || c.ExpireAt.Unix() != 1792273297 {
				t.Errorf("marzban stores seconds; expiry = %v", c.ExpireAt)
			}
		case "mina":
			if c.Enable {
				t.Error("a disabled marzban user came in enabled")
			}
			if c.ExpireAt != nil {
				t.Error("a null expire became a date")
			}
		}
	}
}

func TestMarzbanExclusionsAreHonoured(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "xray_config.json")
	cfg := fmt.Sprintf(`{"inbounds":[{"tag":"VLESS_REALITY","protocol":"vless",
		"port":8443,"streamSettings":%s}]}`, realityStream)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadXrayConfig(cfgPath)
	MarzbanXray = loaded
	t.Cleanup(func() { MarzbanXray = nil })

	path := marzbanDB(t)
	db, _ := sql.Open("sqlite", "file:"+path)
	if _, err := db.Exec(
		`INSERT INTO exclude_inbounds_association VALUES (1,'VLESS_REALITY')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	snap := readSnapshot(t, path, Options{})
	for _, c := range snap.Inbounds[0].Clients {
		if c.Email == "sara" {
			t.Error("sara was excluded from this inbound but got imported onto it")
		}
	}
	if !hasSkip(snap, "sara") {
		t.Errorf("the exclusion should be reported, got %+v", snap.Skipped)
	}
}

/* ── detection ───────────────────────────────────────────────────────── */

func TestUnknownDatabaseIsRejected(t *testing.T) {
	path := newSourceDB(t, `CREATE TABLE something (id integer)`)
	if _, err := Open(path, ""); err == nil {
		t.Fatal("expected an unrecognised database to be refused")
	}
}

func TestMissingFileIsReportedClearly(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.db"), ""); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestDetectionNamesTheLayout(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{xuiNormalised(t), "normalised"},
		{xuiSettings(t), "inbound settings"},
	} {
		src, err := Open(tc.path, "")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(src.Variant, tc.want) {
			t.Errorf("variant = %q, want something mentioning %q", src.Variant, tc.want)
		}
		src.Close()
	}
}

/* ── helpers ─────────────────────────────────────────────────────────── */

func hasWarning(s *Snapshot, substr string) bool {
	for _, w := range s.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func hasSkip(s *Snapshot, substr string) bool {
	for _, k := range s.Skipped {
		if strings.Contains(k.Ref, substr) || strings.Contains(k.Reason, substr) {
			return true
		}
	}
	return false
}
