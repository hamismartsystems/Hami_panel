package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func TestBulkTextListsEveryConfigBetweenDividers(t *testing.T) {
	p := Plans[0]
	o := &store.Order{ID: 9, GB: 10, Quantity: 3}
	items := []bulkItem{
		{Seq: 1, Email: "hami-9-01-a", SubURL: "https://sub/x1", Link: "vless://one"},
		{Seq: 2, Email: "hami-9-02-b", SubURL: "https://sub/x2", Link: "vless://two"},
		{Seq: 3, Email: "hami-9-03-c", Link: "vless://three"},
	}
	text := bulkText(p, o, &store.BotUser{}, items, "https://t.me/support")
	for _, want := range []string{"vless://one", "vless://two", "vless://three",
		"https://sub/x1", "https://sub/x2", "کانفیگ 3 از 3"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text is missing %q", want)
		}
	}
	// One divider before each config, and one closing it.
	if got := strings.Count(text, bulkDivider); got != len(items)+1 {
		t.Fatalf("dividers = %d, want %d", got, len(items)+1)
	}
	// Order is preserved: config 1 is printed before config 3.
	if strings.Index(text, "vless://one") > strings.Index(text, "vless://three") {
		t.Fatal("configs are out of order")
	}
}

func TestBulkExportIsPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "exports")
	b := &Bot{cfg: Config{ExportDir: dir}}
	path, err := b.saveBulkExport(42, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "order-42.txt" {
		t.Fatalf("unexpected file name %s", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %o, want 700", di.Mode().Perm())
	}

	off := &Bot{cfg: Config{}}
	if p, err := off.saveBulkExport(1, "x"); p != "" || err != nil {
		t.Fatalf("disabled export should write nothing, got %q %v", p, err)
	}
}
