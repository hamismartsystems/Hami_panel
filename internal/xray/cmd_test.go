package xray_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenCommandPrintsLinkAndWritesConfig(t *testing.T) {
	dir := t.TempDir()
	spec := filepath.Join(dir, "spec.json")
	out := filepath.Join(dir, "config.json")
	body := `{
	  "inbounds": [{
	    "remark": "hami",
	    "protocol": "vless",
	    "port": 443,
	    "host": "203.0.113.10",
	    "transport": "tcp",
	    "security": "reality",
	    "sni": "www.samsung.com",
	    "publicKey": "PUB_ONLY",
	    "privateKey": "PRIV_ONLY",
	    "shortId": "abcd",
	    "dest": "www.samsung.com:443",
	    "flow": "xtls-rprx-vision",
	    "clients": [{"uuid": "11111111-1111-1111-1111-111111111111", "email": "a@hami"}]
	  }]
	}`
	if err := os.WriteFile(spec, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", ".", "gen", "-spec", spec, "-out", out)
	cmd.Dir = filepath.Join("..", "..", "cmd", "hami")
	// This test file lives in internal/xray, so the module root is two up
	// only when `go test` is run from that package. Use the module via the
	// test binary's working directory instead.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = filepath.Clean(filepath.Join(wd, "..", "..", "cmd", "hami"))
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hami gen: %v\n%s", err, raw)
	}
	link := string(raw)
	if !strings.Contains(link, "vless://") || !strings.Contains(link, "PUB_ONLY") {
		t.Fatalf("stdout: %s", link)
	}
	if strings.Contains(link, "PRIV_ONLY") {
		t.Fatal("private key printed with the link")
	}
	cfg, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "PRIV_ONLY") || strings.Contains(string(cfg), "PUB_ONLY") {
		t.Fatalf("config: %s", cfg)
	}
}
