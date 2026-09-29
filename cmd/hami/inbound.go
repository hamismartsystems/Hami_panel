package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// inboundCmd manages listening endpoints in the panel database:
//
//	hami inbound add -db panel.db -remark NAME -protocol vless -port 443 -host HOST
//	                 [-transport tcp] [-security reality] [-sni X] [-pbk PK]
//	                 [-sid SID] [-spx /] [-fp chrome] [-path /x] [-xhttp-mode auto]
//	                 [-header-type none] [-flow FLOW] [-private-key K] [-dest D] [-listen IP]
//	hami inbound list -db panel.db
//	hami inbound enable|disable -db panel.db -id N
//	hami inbound delete -db panel.db -id N
//
// Only the parameters clients see in links live here. Core-side secrets
// (Reality private key, cert paths) go to inbound_secrets via flags so the
// generated core config and the public links stay consistent.
func inboundCmd(args []string) int {
	if len(args) < 1 {
		inboundUsage()
		return 2
	}
	switch args[0] {
	case "add":
		return inboundAdd(args[1:])
	case "list":
		return userInbounds(args[1:])
	case "enable", "disable":
		return inboundToggle(args[0], args[1:])
	case "delete":
		return inboundDelete(args[1:])
	}
	inboundUsage()
	return 2
}

func inboundUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami inbound add -db panel.db -remark NAME -protocol PROTO -port N -host HOST [flags]
  hami inbound list -db panel.db
  hami inbound enable|disable -db panel.db -id N
  hami inbound delete -db panel.db -id N
`)
}

func inboundAdd(args []string) int {
	fs := flag.NewFlagSet("inbound add", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	remark := fs.String("remark", "", "")
	protocol := fs.String("protocol", "", "")
	port := fs.Int("port", 0, "")
	host := fs.String("host", "", "")
	nodeRef := fs.String("node", "", "attach to node (name or id)")
	transport := fs.String("transport", "tcp", "")
	security := fs.String("security", "none", "")
	sni := fs.String("sni", "", "")
	pbk := fs.String("pbk", "", "")
	sid := fs.String("sid", "", "")
	spx := fs.String("spx", "", "")
	fp := fs.String("fp", "chrome", "")
	path := fs.String("path", "", "")
	xhttpMode := fs.String("xhttp-mode", "auto", "")
	headerType := fs.String("header-type", "none", "")
	flow := fs.String("flow", "", "")
	privKey := fs.String("private-key", "", "reality private key (core-side)")
	dest := fs.String("dest", "", "reality dest, e.g. www.microsoft.com:443")
	listen := fs.String("listen", "0.0.0.0", "")
	certFile := fs.String("cert-file", "", "")
	keyFile := fs.String("key-file", "", "")
	obfsType := fs.String("obfs-type", "", "hysteria2 obfs type (salamander)")
	obfsPassword := fs.String("obfs-password", "", "hysteria2 obfs password")
	alpn := fs.String("alpn", "", "alpn, e.g. h3 for tuic/hysteria2")
	cc := fs.String("cc", "", "congestion control for tuic: bbr/cubic/new_reno")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
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
	in := &store.Inbound{
		NodeID: nodeID,
		Remark: *remark, Protocol: *protocol, Port: *port, Host: *host,
		Transport: *transport, Security: *security, SNI: *sni,
		PublicKey: *pbk, ShortID: *sid, SpiderX: *spx, Fingerprint: *fp,
		Path: *path, XHTTPMode: *xhttpMode, HeaderType: *headerType, Flow: *flow,
		ObfsType: *obfsType, ObfsPassword: *obfsPassword, Alpn: *alpn, CongestionControl: *cc,
	}
	if err := st.CreateInbound(in); err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		return 1
	}
	// secrets stay out of links; they serve the core config
	if *privKey != "" || *dest != "" || *certFile != "" {
		if err := st.SetInboundSecret(store.InboundSecret{
			InboundID: in.ID, PrivateKey: *privKey, Dest: *dest,
			Listen: *listen, CertFile: *certFile, KeyFile: *keyFile,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "secrets: %v\n", err)
			return 1
		}
	}
	logEvent(*dbPath, "info", "inbound", fmt.Sprintf("added %s (%s %d)", *remark, *protocol, *port), "")
	fmt.Printf("✅ created inbound %d (%s %s:%d %s/%s)\n", in.ID, *remark, *host, *port, *transport, *security)
	return 0
}

// resolveNodeID accepts "-node fr-1", "-node 3", or "" (local, id 0).
func resolveNodeID(st *store.Store, ref string) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	if n, err := st.GetNodeByName(ref); err == nil {
		return n.ID, nil
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("no node named %q (use `hami node add` first, or pass a numeric id)", ref)
	}
	n, err := st.GetNode(id)
	if err != nil {
		return 0, fmt.Errorf("no node with id %d", id)
	}
	return n.ID, nil
}

func inboundToggle(verb string, args []string) int {
	fs := flag.NewFlagSet("inbound "+verb, flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	id := fs.Int64("id", 0, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *id == 0 {
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.SetInboundEnabled(*id, verb == "enable"); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", verb, err)
		return 1
	}
	logEvent(*dbPath, "info", "inbound", fmt.Sprintf("%s inbound %d", verb+dSuffix(verb), *id), "")
	fmt.Printf("✅ %s inbound %d\n", verb+dSuffix(verb), *id)
	return 0
}

func inboundDelete(args []string) int {
	fs := flag.NewFlagSet("inbound delete", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	id := fs.Int64("id", 0, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *id == 0 {
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	if err := st.DeleteInbound(*id); err != nil {
		fmt.Fprintf(os.Stderr, "delete: %v\n", err)
		return 1
	}
	logEvent(*dbPath, "warn", "inbound", fmt.Sprintf("deleted inbound %d", *id), "")
	fmt.Printf("✅ deleted inbound %d\n", *id)
	return 0
}
