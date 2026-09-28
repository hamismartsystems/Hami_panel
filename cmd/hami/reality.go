package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/reality"
)

func realityCmd(args []string) int {
	if len(args) < 1 {
		realityUsage()
		return 2
	}
	switch args[0] {
	case "check":
		return realityCheck(args[1:])
	case "rotate":
		return realityRotate(args[1:])
	case "keygen":
		return realityKeygen(args[1:])
	}
	realityUsage()
	return 2
}

func realityUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami reality check -dest HOST:PORT -sni SNI
  hami reality keygen
  hami reality rotate -db panel.db -id INBOUND_ID [-new-sid] [-new-key]
`)
}

func realityCheck(args []string) int {
	fs := flag.NewFlagSet("reality check", flag.ContinueOnError)
	dest := fs.String("dest", "", "reality dest, e.g. www.microsoft.com:443")
	sni := fs.String("sni", "", "SNI, e.g. www.microsoft.com")
	timeout := fs.Duration("timeout", 6*time.Second, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dest == "" {
		realityUsage()
		return 2
	}
	res := reality.CheckDest(*dest, *sni, *timeout)
	if res.OK {
		fmt.Printf("✅ %s (SNI=%s) — %s\n", res.Dest, res.SNI, res.Detail)
		if len(res.Resolved) > 0 {
			fmt.Printf("   resolved: %v\n", res.Resolved)
		}
		return 0
	}
	fmt.Fprintf(os.Stderr, "❌ %s (SNI=%s) — %s\n", res.Dest, res.SNI, res.Detail)
	if res.CertCN != "" {
		fmt.Fprintf(os.Stderr, "   cert CN: %s\n", res.CertCN)
	}
	return 1
}

func realityKeygen(args []string) int {
	fs := flag.NewFlagSet("reality keygen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.Parse(args)
	priv, pub, err := reality.GenerateKeypair()
	if err != nil {
		fmt.Fprintf(os.Stderr, "keygen: %v\n", err)
		return 1
	}
	sid, err := reality.GenerateShortID(4)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shortid: %v\n", err)
		return 1
	}
	fmt.Printf("private: %s\npublic:  %s\nshortId: %s\n", priv, pub, sid)
	return 0
}

func realityRotate(args []string) int {
	fs := flag.NewFlagSet("reality rotate", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	id := fs.Int64("id", 0, "")
	newSID := fs.Bool("new-sid", true, "rotate ShortID")
	newKey := fs.Bool("new-key", false, "rotate private/public keypair (clients need new link)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *id == 0 {
		realityUsage()
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()

	in, err := st.GetInbound(*id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "get inbound: %v\n", err)
		return 1
	}
	if in.Security != "reality" {
		fmt.Fprintf(os.Stderr, "inbound %d is not reality (security=%s)\n", *id, in.Security)
		return 1
	}
	sec, err := st.GetInboundSecret(*id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "secret: %v\n", err)
		return 1
	}

	// backup old values in audit log
	logEvent(*dbPath, "info", "reality", fmt.Sprintf("rotate inbound %d old pbk=%s sid=%s", *id, in.PublicKey, in.ShortID), "")

	if *newSID {
		sid, err := reality.GenerateShortID(4)
		if err != nil {
			fmt.Fprintf(os.Stderr, "shortid: %v\n", err)
			return 1
		}
		in.ShortID = sid
		// update inbound row - we need to do it via SQL because Store has no UpdateInbound
		// for now, delete and recreate is too heavy, so we update directly
		if err := st.UpdateInboundReality(in.ID, in.PublicKey, in.ShortID, in.SNI, in.Fingerprint, in.SpiderX); err != nil {
			fmt.Fprintf(os.Stderr, "update inbound: %v\n", err)
			return 1
		}
		// also update shortIds list in secret to include new sid
		// keep old shortIds + new one for graceful rotation
		// we store shortIds as JSON? For now, secret only has single dest, not shortIds list.
		// The xray config uses shortIDs from endpoint, which includes inbound.ShortID.
		// So rotating ShortID is enough.
		fmt.Printf("✅ new ShortID: %s\n", sid)
	}

	if *newKey {
		priv, pub, err := reality.GenerateKeypair()
		if err != nil {
			fmt.Fprintf(os.Stderr, "keypair: %v\n", err)
			return 1
		}
		in.PublicKey = pub
		if err := st.UpdateInboundReality(in.ID, pub, in.ShortID, in.SNI, in.Fingerprint, in.SpiderX); err != nil {
			fmt.Fprintf(os.Stderr, "update inbound: %v\n", err)
			return 1
		}
		sec.PrivateKey = priv
		if err := st.SetInboundSecret(sec); err != nil {
			fmt.Fprintf(os.Stderr, "secret: %v\n", err)
			return 1
		}
		fmt.Printf("✅ new keypair\n   private kept server-side\n   public: %s\n", pub)
		fmt.Printf("   ⚠️  clients need new links (pbk changed)\n")
	}

	logEvent(*dbPath, "warn", "reality", fmt.Sprintf("rotated inbound %d new pbk=%s sid=%s", *id, in.PublicKey, in.ShortID), "")
	return 0
}
