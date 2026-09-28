package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hamismartsystems/hami_panel/internal/reality"
	"github.com/hamismartsystems/hami_panel/internal/store"
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
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *tName == "" || *remark == "" || *host == "" || *port == 0 {
		templateUsage()
		return 2
	}
	tmpl, err := tpl.ByName(*tName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
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

	// auto-generate Reality keys if template needs them and none provided
	var privKey, pubKey, shortID string
	if tmpl.Security == "reality" {
		privKey = *priv
		pubKey = *pbk
		shortID = *sid
		if privKey == "" || pubKey == "" {
			pk, pub, err := reality.GenerateKeypair()
			if err != nil {
				fmt.Fprintf(os.Stderr, "keypair: %v\n", err)
				return 1
			}
			if privKey == "" {
				privKey = pk
			}
			if pubKey == "" {
				pubKey = pub
			}
		}
		if shortID == "" {
			s, err := reality.GenerateShortID(4)
			if err != nil {
				fmt.Fprintf(os.Stderr, "shortid: %v\n", err)
				return 1
			}
			shortID = s
		}
	}

	in := &store.Inbound{
		NodeID:      nodeID,
		Remark:      *remark,
		Protocol:    tmpl.Protocol,
		Port:        *port,
		Host:        *host,
		Transport:   tmpl.Transport,
		Security:    tmpl.Security,
		SNI:         *sni,
		PublicKey:   pubKey,
		ShortID:     shortID,
		Fingerprint: tmpl.Fingerprint,
		Path:        tmpl.Path,
		XHTTPMode:   tmpl.XHTTPMode,
		HeaderType:  tmpl.HeaderType,
		Flow:        tmpl.Flow,
	}
	if err := st.CreateInbound(in); err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		return 1
	}
	if tmpl.Security == "reality" {
		if err := st.SetInboundSecret(store.InboundSecret{
			InboundID:  in.ID,
			PrivateKey: privKey,
			Dest:       *dest,
			Listen:     *listen,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "secret: %v\n", err)
			return 1
		}
	}
	logEvent(*dbPath, "info", "template", fmt.Sprintf("applied %s as inbound %d (%s)", tmpl.Name, in.ID, *remark), "")
	fmt.Printf("✅ template %s -> inbound %d (pbk=%s sid=%s)\n", tmpl.Name, in.ID, pubKey, shortID)
	if tmpl.Security == "reality" && privKey != "" {
		fmt.Printf("   private key kept server-side only\n")
	}
	return 0
}
