package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/hamismartsystems/hami_panel/internal/importer"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// importCmd migrates another panel into HAMI:
//
//	hami import scan  -src /etc/x-ui/x-ui.db
//	hami import plan  -db panel.db -src /etc/x-ui/x-ui.db
//	hami import apply -db panel.db -src /etc/x-ui/x-ui.db -yes
//
// scan and plan never write anything. apply refuses to run without -yes,
// copies the destination database aside first, and reads everything back
// afterwards to prove it landed.
func importCmd(args []string) int {
	if len(args) < 1 {
		importUsage()
		return 2
	}
	switch args[0] {
	case "scan":
		return importScan(args[1:])
	case "plan":
		return importRun(args[1:], false)
	case "apply":
		return importRun(args[1:], true)
	}
	importUsage()
	return 2
}

func importUsage() {
	fmt.Fprint(os.Stderr, `usage:
  hami import scan  -src PATH [-from 3x-ui|x-ui|marzban]
  hami import plan  -db panel.db -src PATH [-host ADDR] [-node NAME] [-keep-subs]
  hami import apply -db panel.db -src PATH [-host ADDR] [-node NAME] [-keep-subs] -yes

  -src          the other panel's database
                  3x-ui / x-ui   /etc/x-ui/x-ui.db
                  marzban        /var/lib/marzban/db.sqlite3
  -xray-config  marzban only: xray_config.json, which holds the real
                inbound definitions
  -host         address clients should dial, when the source advertises
                the wrong one
  -node         put the imported inbounds on this node
  -flow         force the inbound flow (e.g. xtls-rprx-vision, or "none")
                when the source mixes flows between clients
  -keep-subs    keep each client's subscription id, so links already in
                customers' apps keep working

The source panel is never written to: it is copied first, the copy is
opened read-only, and the original is checksummed before and after.
`)
}

type importFlags struct {
	db, src, from, host, node, xray, flow string
	keepSubs, yes                         bool
}

func importFlagSet(name string, f *importFlags, withDB bool) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	if withDB {
		fs.StringVar(&f.db, "db", "", "")
	}
	fs.StringVar(&f.src, "src", "", "")
	fs.StringVar(&f.from, "from", "", "")
	fs.StringVar(&f.host, "host", "", "")
	fs.StringVar(&f.node, "node", "", "")
	fs.StringVar(&f.xray, "xray-config", "", "")
	fs.StringVar(&f.flow, "flow", "", "")
	fs.BoolVar(&f.keepSubs, "keep-subs", false, "")
	fs.BoolVar(&f.yes, "yes", false, "")
	fs.SetOutput(os.Stderr)
	return fs
}

func importOpen(f importFlags) (*importer.Opened, error) {
	if f.src == "" {
		return nil, fmt.Errorf("-src is required")
	}
	if f.xray != "" {
		cfg, err := importer.LoadXrayConfig(f.xray)
		if err != nil {
			return nil, err
		}
		importer.MarzbanXray = cfg
	}
	return importer.Open(f.src, importer.Kind(f.from))
}

func importScan(args []string) int {
	var f importFlags
	fs := importFlagSet("import scan", &f, false)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	src, err := importOpen(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer src.Close()

	snap, err := src.Read(importer.Options{
		Host: f.host, KeepSubTokens: f.keepSubs, Flow: f.flow})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("source:   %s\n", src.Original)
	fmt.Printf("panel:    %s (%s)\n", snap.Kind, snap.Variant)
	fmt.Printf("checksum: %s… (verified unchanged after reading)\n", src.Digest[:16])
	fmt.Println()
	printSnapshot(snap)
	return 0
}

func importRun(args []string, doApply bool) int {
	name := "import plan"
	if doApply {
		name = "import apply"
	}
	var f importFlags
	fs := importFlagSet(name, &f, true)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if f.db == "" {
		fmt.Fprintln(os.Stderr, "error: -db is required")
		return 2
	}

	src, err := importOpen(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer src.Close()

	st, err := store.Open(f.db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer st.Close()

	opt := importer.Options{Host: f.host, KeepSubTokens: f.keepSubs, Flow: f.flow}
	if f.node != "" {
		n, err := st.GetNodeByName(f.node)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: node %q: %v\n", f.node, err)
			return 1
		}
		opt.NodeID = n.ID
	}

	snap, err := src.Read(opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	plan, err := importer.BuildPlan(st, snap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf("source:   %s\n", src.Original)
	fmt.Printf("panel:    %s (%s)\n", snap.Kind, snap.Variant)
	fmt.Println()
	printSnapshot(snap)
	printPlan(plan)

	newIn, newCl, oldIn, oldCl := plan.Counts()
	if !doApply {
		fmt.Printf("\nnothing was written. to go ahead:\n  hami import apply -db %s -src %s%s -yes\n",
			f.db, f.src, importExtraFlags(f))
		return 0
	}
	if len(plan.Conflicts) > 0 {
		fmt.Fprintln(os.Stderr, "\nrefusing to import while there are conflicts above.")
		return 1
	}
	if !f.yes {
		fmt.Fprintf(os.Stderr,
			"\nthis would create %d inbounds and %d clients. re-run with -yes to do it.\n",
			newIn, newCl)
		return 1
	}
	if newIn == 0 && newCl == 0 {
		fmt.Printf("\nnothing new to import (%d inbounds and %d clients are already here).\n",
			oldIn, oldCl)
		return 0
	}

	res, err := importer.Apply(st, f.db, plan)
	if res != nil && res.BackupPath != "" {
		fmt.Printf("\nthe database as it was before this run: %s\n", res.BackupPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("imported %d inbounds and %d clients", res.CreatedInbounds, res.CreatedClients)
	if res.Skipped > 0 {
		fmt.Printf(", left %d already-imported clients alone", res.Skipped)
	}
	fmt.Println(".")
	if res.Verified {
		fmt.Println("read back and verified: quotas, usage and expiry all match the source.")
	}
	_ = st.AddEvent("info", "import", fmt.Sprintf(
		"imported %d inbounds and %d clients from %s", res.CreatedInbounds,
		res.CreatedClients, snap.Kind), "src="+f.src)
	return 0
}

func importExtraFlags(f importFlags) string {
	var b strings.Builder
	if f.host != "" {
		fmt.Fprintf(&b, " -host %s", f.host)
	}
	if f.node != "" {
		fmt.Fprintf(&b, " -node %s", f.node)
	}
	if f.keepSubs {
		b.WriteString(" -keep-subs")
	}
	if f.flow != "" {
		fmt.Fprintf(&b, " -flow %s", f.flow)
	}
	if f.xray != "" {
		fmt.Fprintf(&b, " -xray-config %s", f.xray)
	}
	return b.String()
}

func printSnapshot(snap *importer.Snapshot) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INBOUND\tPORT\tPROTO\tSECURITY\tHOST\tCLIENTS")
	for _, in := range snap.Inbounds {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%d\n", in.Inbound.Remark, in.Inbound.Port,
			in.Inbound.Protocol, in.Inbound.Security, in.Inbound.Host, len(in.Clients))
	}
	w.Flush()

	if len(snap.Warnings) > 0 {
		fmt.Printf("\n%d thing(s) to look at:\n", len(snap.Warnings))
		for _, m := range snap.Warnings {
			fmt.Println("  -", m)
		}
	}
	if len(snap.Skipped) > 0 {
		fmt.Printf("\n%d thing(s) not imported:\n", len(snap.Skipped))
		for _, s := range snap.Skipped {
			fmt.Printf("  - %s %s: %s\n", s.What, s.Ref, s.Reason)
		}
	}
}

func printPlan(p *importer.Plan) {
	newIn, newCl, oldIn, oldCl := p.Counts()
	fmt.Printf("\nplan: create %d inbounds and %d clients", newIn, newCl)
	if oldIn > 0 || oldCl > 0 {
		fmt.Printf("; %d inbounds and %d clients are already here and will be left alone",
			oldIn, oldCl)
	}
	fmt.Println(".")
	for _, in := range p.Inbounds {
		if in.Action == importer.Exists {
			fmt.Printf("  = %-24s %s\n", in.Inbound.Remark, in.Reason)
		}
	}
	if len(p.Conflicts) > 0 {
		fmt.Printf("\n%d conflict(s) — these need a decision from you:\n", len(p.Conflicts))
		for _, c := range p.Conflicts {
			fmt.Println("  -", c)
		}
	}
}
