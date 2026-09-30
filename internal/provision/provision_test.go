package provision

import (
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

func openMem(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func realityOpts() InboundOptions {
	return InboundOptions{
		Template: "vless-reality-tcp", Remark: "Reality-443",
		Host: "198.51.100.10", Port: 443, SNI: "www.samsung.com",
	}
}

func TestInboundFromTemplateFillsRealityMaterial(t *testing.T) {
	s := openMem(t)
	in, err := Inbound(s, realityOpts())
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if in.Protocol != "vless" || in.Transport != "tcp" || in.Security != "reality" {
		t.Fatalf("template not applied: %+v", in)
	}
	if in.PublicKey == "" || in.ShortID == "" {
		t.Fatal("reality public key and short id must be generated")
	}
	if in.Flow != "xtls-rprx-vision" {
		t.Errorf("flow from the template was lost: %q", in.Flow)
	}
	if !in.Enable {
		t.Error("a new inbound should be enabled")
	}
}

// The private key is the one secret that must never be reachable from the
// row the link builder reads.
func TestPrivateKeyStaysInTheSecretsTable(t *testing.T) {
	s := openMem(t)
	in, err := Inbound(s, realityOpts())
	if err != nil {
		t.Fatal(err)
	}
	sec, err := s.GetInboundSecret(in.ID)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if sec.PrivateKey == "" {
		t.Fatal("the private key was not stored server side")
	}
	if sec.PrivateKey == in.PublicKey {
		t.Fatal("public and private key are the same value")
	}
	fetched, err := s.GetInbound(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Nothing on the inbound row may equal the private key.
	for name, v := range map[string]string{
		"PublicKey": fetched.PublicKey, "ShortID": fetched.ShortID,
		"SNI": fetched.SNI, "Path": fetched.Path, "Fingerprint": fetched.Fingerprint,
	} {
		if v != "" && v == sec.PrivateKey {
			t.Fatalf("field %s leaks the private key", name)
		}
	}
}

func TestDestDefaultsToTheSNI(t *testing.T) {
	s := openMem(t)
	in, err := Inbound(s, realityOpts()) // no Dest given
	if err != nil {
		t.Fatal(err)
	}
	sec, _ := s.GetInboundSecret(in.ID)
	if sec.Dest != "www.samsung.com:443" {
		t.Fatalf("want the SNI on 443, got %q", sec.Dest)
	}
}

func TestRealityWithoutSNIIsRefused(t *testing.T) {
	s := openMem(t)
	o := realityOpts()
	o.SNI = ""
	if _, err := Inbound(s, o); err == nil {
		t.Fatal("a Reality inbound with no SNI builds a link that never connects; it must be refused")
	}
}

func TestInvalidInboundInputIsRefused(t *testing.T) {
	s := openMem(t)
	cases := map[string]func(*InboundOptions){
		"no template":      func(o *InboundOptions) { o.Template = "" },
		"unknown template": func(o *InboundOptions) { o.Template = "does-not-exist" },
		"no name":          func(o *InboundOptions) { o.Remark = "" },
		"no host":          func(o *InboundOptions) { o.Host = "" },
		"port zero":        func(o *InboundOptions) { o.Port = 0 },
		"port high":        func(o *InboundOptions) { o.Port = 70000 },
		"bad node":         func(o *InboundOptions) { o.NodeID = 999 },
	}
	for name, mutate := range cases {
		o := realityOpts()
		mutate(&o)
		if _, err := Inbound(s, o); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDuplicatePortOnSameNodeIsRefused(t *testing.T) {
	s := openMem(t)
	if _, err := Inbound(s, realityOpts()); err != nil {
		t.Fatal(err)
	}
	o := realityOpts()
	o.Remark = "another one"
	_, err := Inbound(s, o)
	if err == nil {
		t.Fatal("two inbounds on the same port and node must be refused")
	}
	if !strings.Contains(err.Error(), "already used") {
		t.Errorf("the error should name the conflict, got %q", err)
	}
	// a different port is fine
	o.Port = 8443
	if _, err := Inbound(s, o); err != nil {
		t.Fatalf("a free port should be accepted: %v", err)
	}
}

func TestUserGetsUUIDQuotaExpiryAndToken(t *testing.T) {
	s := openMem(t)
	in, err := Inbound(s, realityOpts())
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	c, err := User(s, UserOptions{InboundID: in.ID, Email: "ali", QuotaGB: 50, Days: 30})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if c.UUID == "" || len(c.UUID) < 30 {
		t.Fatalf("a uuid should be generated, got %q", c.UUID)
	}
	if c.TotalBytes != 50*(1<<30) {
		t.Errorf("quota: want 50 GiB in bytes, got %d", c.TotalBytes)
	}
	if c.ExpireAt == nil {
		t.Fatal("expiry was not set")
	}
	if d := c.ExpireAt.Sub(before).Hours(); d < 29*24 || d > 31*24 {
		t.Errorf("expiry should be about 30 days out, got %.1f hours", d)
	}
	if c.SubToken == "" {
		t.Fatal("no subscription token was issued")
	}
	// the token must resolve back to this exact client
	got, err := s.ClientBySubToken(c.SubToken)
	if err != nil || got == nil || got.ID != c.ID {
		t.Fatalf("token does not resolve to the client: %v %+v", err, got)
	}
	// and the link must build from this client's own inbound
	link, err := subs.LinkOf(subs.Entry{Inbound: *in, Client: *c})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if !strings.Contains(link, c.UUID) || !strings.Contains(link, "security=reality") {
		t.Fatalf("unexpected link: %s", link)
	}
}

func TestZeroQuotaAndZeroDaysMeanUnlimited(t *testing.T) {
	s := openMem(t)
	in, _ := Inbound(s, realityOpts())
	c, err := User(s, UserOptions{InboundID: in.ID, Email: "forever"})
	if err != nil {
		t.Fatal(err)
	}
	if c.TotalBytes != 0 {
		t.Errorf("quota 0 must mean unlimited, got %d", c.TotalBytes)
	}
	if c.ExpireAt != nil {
		t.Errorf("0 days must mean no expiry, got %v", c.ExpireAt)
	}
}

func TestInvalidUserInputIsRefused(t *testing.T) {
	s := openMem(t)
	in, _ := Inbound(s, realityOpts())
	if _, err := User(s, UserOptions{InboundID: in.ID, Email: "ali"}); err != nil {
		t.Fatal(err)
	}
	cases := map[string]UserOptions{
		"duplicate name":  {InboundID: in.ID, Email: "ali"},
		"empty name":      {InboundID: in.ID, Email: "  "},
		"name with space": {InboundID: in.ID, Email: "two words"},
		"name with slash": {InboundID: in.ID, Email: "a/b"},
		"no inbound":      {Email: "someone"},
		"unknown inbound": {InboundID: 999, Email: "someone"},
		"negative quota":  {InboundID: in.ID, Email: "neg", QuotaGB: -1},
		"negative days":   {InboundID: in.ID, Email: "neg2", Days: -5},
	}
	for name, o := range cases {
		if _, err := User(s, o); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestGBRoundTrip(t *testing.T) {
	for _, gb := range []float64{0, 0.5, 1, 50, 1024} {
		if got := BytesToGB(GBToBytes(gb)); got != gb {
			t.Errorf("round trip %v gb -> %v", gb, got)
		}
	}
}
