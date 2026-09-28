package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func TestBackupRestore(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panel.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// create one inbound so db is not empty
	if err := st.CreateInbound(&store.Inbound{Protocol: "vless", Port: 443, Host: "example.com", Remark: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvent("info", "test", "hello", ""); err != nil {
		t.Fatal(err)
	}
	st.Close()

	xrayDir := filepath.Join(dir, "xray")
	if err := os.MkdirAll(xrayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xrayDir, "PIN"), []byte(`{"version":"26.3.27","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "backup.tar.gz")
	if err := Backup(dbPath, xrayDir, out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}

	// restore to new location
	restoreDB := filepath.Join(dir, "restored.db")
	restoreXray := filepath.Join(dir, "restored-xray")
	if err := Restore(out, restoreDB, restoreXray); err != nil {
		t.Fatal(err)
	}

	st2, err := store.Open(restoreDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	inbounds, err := st2.ListInbounds()
	if err != nil {
		t.Fatal(err)
	}
	if len(inbounds) != 1 || inbounds[0].Remark != "test" {
		t.Fatalf("inbounds=%+v", inbounds)
	}
	ev, err := st2.RecentEvents(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) == 0 || ev[0].Message != "hello" {
		t.Fatalf("events=%+v", ev)
	}

	pin, err := os.ReadFile(filepath.Join(restoreXray, "PIN"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pin) == 0 {
		t.Fatal("PIN empty")
	}
}

func TestBackupRequiresDB(t *testing.T) {
	if err := Backup("", "", "/tmp/out.tar.gz"); err == nil {
		t.Fatal("should fail")
	}
	if err := Backup("/nonexistent.db", "", "/tmp/out.tar.gz"); err == nil {
		t.Fatal("should fail")
	}
}
