package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hamismartsystems/hami_panel/internal/provision"
	tpl "github.com/hamismartsystems/hami_panel/internal/template"
)

func templateCmd(args []string) int {
	if len(args) < 1 {
		templateUsage()
		return 2
	}
	switch args[0] {
	case "list":
		return templateList(args[1:])
	case "apply":
		return templateApply(args[1:])
	}
	templateUsage()
	return 2
}

func templateUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami template list
  hami template apply -db panel.db -template NAME -remark REMARK -host HOST -port PORT -sni SNI -dest DEST [-pbk PUB] [-priv PRIVATE] [-sid SID] [-node NAME|ID]
`)
}

func templateList(args []string) int {
	fs := flag.NewFlagSet("template list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.Parse(args)
	for _, t := range tpl.All() {
		fmt.Printf("%-28s %s/%s/%s — %s\n", t.Name, t.Protocol, t.Transport, t.Security, t.Description)
	}
	return 0
}

func templateApply(args []string) int {
	fs := flag.NewFlagSet("template apply", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	tName := fs.String("template", "", "")
	remark := fs.String("remark", "", "")
	host := fs.String("host", "", "")
	port := fs.Int("port", 0, "")
	sni := fs.String("sni", "", "")
	dest := fs.String("dest", "", "")
	pbk := fs.String("pbk", "", "reality public key, auto-generated if empty")
	priv := fs.String("private-key", "", "reality private key, auto-generated if empty")
	sid := fs.String("sid", "", "short id, auto-generated if empty")
	nodeRef := fs.String("node", "", "")
	listen := fs.String("listen", "0.0.0.0", "")
	certFile := fs.String("cert-file", "", "tls cert path for sing-box")
	keyFile := fs.String("key-file", "", "tls key path for sing-box")
	obfsType := fs.String("obfs-type", "", "hysteria2 obfs type")
	obfsPassword := fs.String("obfs-password", "", "hysteria2 obfs password")
	alpn := fs.String("alpn", "", "alpn for sing-box")
	cc := fs.String("cc", "", "congestion control for tuic")
	isPrivate := fs.Bool("private", false, "mark as private/dedicated (admin-only)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *tName == "" || *remark == "" || *host == "" || *port == 0 {
		templateUsage()
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()

	nodeID, err := resolveNodeID(st, *nodeRef)
	if err != nil {
		fmt.Fprintf(os.Stderr, "node: %v\n", err)
		return 1
	}

	in, err := provision.Inbound(st, provision.InboundOptions{
		Template: *tName, Remark: *remark, Host: *host, Port: *port,
		SNI: *sni, Dest: *dest, NodeID: nodeID, Listen: *listen,
		CertFile: *certFile, KeyFile: *keyFile, Private: *isPrivate,
		PrivateKey: *priv, PublicKey: *pbk, ShortID: *sid,
		ObfsType: *obfsType, ObfsPassword: *obfsPassword,
		Alpn: *alpn, CongestionControl: *cc,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}
	tmpl, _ := tpl.ByName(*tName)
	logEvent(*dbPath, "info", "template", fmt.Sprintf("applied %s as inbound %d (%s)", tmpl.Name, in.ID, *remark), "")
	fmt.Printf("✅ template %s -> inbound %d (pbk=%s sid=%s)\n", tmpl.Name, in.ID, in.PublicKey, in.ShortID)
	if tmpl.Security == "reality" {
		fmt.Printf("   private key kept server-side only\n")
	}
	return 0
}
