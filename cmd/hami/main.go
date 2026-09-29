// Command hami generates an Xray config and the matching client links from
// one spec. Each link is built only from the inbound that client is on.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/backup"
	"github.com/hamismartsystems/hami_panel/internal/guard"
	"github.com/hamismartsystems/hami_panel/internal/singbox"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "gen":
		os.Exit(gen(os.Args[2:]))
	case "canary":
		os.Exit(canary(os.Args[2:]))
	case "guard":
		os.Exit(guardCmd(os.Args[2:]))
	case "pin":
		os.Exit(pinCmd(os.Args[2:]))
	case "upgrade":
		os.Exit(upgradeCmd(os.Args[2:]))
	case "backup":
		os.Exit(backupCmd(os.Args[2:]))
	case "restore":
		os.Exit(restoreCmd(os.Args[2:]))
	case "audit":
		os.Exit(auditCmd(os.Args[2:]))
	case "inbound":
		os.Exit(inboundCmd(os.Args[2:]))
	case "user":
		os.Exit(userCmd(os.Args[2:]))
	case "sub":
		os.Exit(subCmd(os.Args[2:]))
	case "notice":
		os.Exit(noticeCmd(os.Args[2:]))
	case "node":
		os.Exit(nodeCmd(os.Args[2:]))
	case "agent":
		os.Exit(agentCmd(os.Args[2:]))
	case "template":
		os.Exit(templateCmd(os.Args[2:]))
	case "reality":
		os.Exit(realityCmd(os.Args[2:]))
	case "web", "hpui", "hp-ui":
		os.Exit(webCmd(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage:\n  hami gen -spec inbounds.json -out config.json [-core xray|singbox]\n  hami canary -spec inbounds.json [-xray /path/to/xray]\n  hami guard -spec inbounds.json [-dial 127.0.0.1] [-db panel.db] [-repair -xray /path/to/xray -config config.json]\n  hami pin [-dir /var/lib/hami]\n  hami upgrade -dir /var/lib/hami -bin ./xray -version 26.3.27\n  hami backup -db panel.db -out backup.tar.gz [-xray-dir /var/lib/hami]\n  hami restore -in backup.tar.gz -db panel.db [-xray-dir /var/lib/hami]\n  hami audit -db panel.db [-tail 50] [-level warn]\n  hami user ... (hami user with no args prints user usage)\n  hami sub serve -db panel.db [-addr :8080] [-base-url URL]\n  hami notice -db panel.db [-days 3] [-ratio 0.8] [-telegram TOKEN:CHATID]\n  hami node add|list|check|remove -db panel.db\n  hami agent run -listen ADDR -name NAME -api-key KEY\n  hami template list\n  hami template apply -db panel.db -template NAME -remark REMARK -host HOST -port PORT -sni SNI -dest DEST ...\n  hami reality check -dest HOST:PORT -sni SNI\n  hami reality keygen\n  hami reality rotate -db panel.db -id ID [-new-sid] [-new-key]\n")
}

func gen(args []string) int {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	specPath := fs.String("spec", "", "inbound spec JSON")
	outPath := fs.String("out", "", "config to write")
	core := fs.String("core", "xray", "core type: xray or singbox")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *specPath == "" || *outPath == "" {
		usage()
		return 2
	}
	raw, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read spec: %v\n", err)
		return 1
	}
	switch *core {
	case "singbox", "sing-box":
		var spec singbox.Spec
		if err := json.Unmarshal(raw, &spec); err != nil {
			fmt.Fprintf(os.Stderr, "spec: %v\n", err)
			return 1
		}
		endpoints, err := spec.Endpoints()
		if err != nil {
			fmt.Fprintf(os.Stderr, "spec: %v\n", err)
			return 1
		}
		cfg, err := singbox.Build(endpoints)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			return 1
		}
		links, err := singbox.Links(endpoints)
		if err != nil {
			fmt.Fprintf(os.Stderr, "link: %v\n", err)
			return 1
		}
		if err := os.WriteFile(*outPath, cfg, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "write config: %v\n", err)
			return 1
		}
		for _, l := range links {
			fmt.Println(l)
		}
		return 0
	default:
		var spec xray.Spec
		if err := json.Unmarshal(raw, &spec); err != nil {
			fmt.Fprintf(os.Stderr, "spec: %v\n", err)
			return 1
		}
		endpoints, err := spec.Endpoints()
		if err != nil {
			fmt.Fprintf(os.Stderr, "spec: %v\n", err)
			return 1
		}
		cfg, err := xray.Build(endpoints)
		if err != nil {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			return 1
		}
		links, err := xray.Links(endpoints)
		if err != nil {
			fmt.Fprintf(os.Stderr, "link: %v\n", err)
			return 1
		}
		if err := os.WriteFile(*outPath, cfg, 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "write config: %v\n", err)
			return 1
		}
		for _, l := range links {
			fmt.Println(l)
		}
		return 0
	}
}

func canary(args []string) int {
	fs := flag.NewFlagSet("canary", flag.ContinueOnError)
	specPath := fs.String("spec", "", "inbound spec JSON")
	xrayBin := fs.String("xray", os.Getenv("XRAY_BIN"), "path to the xray binary")
	dbPath := fs.String("db", "", "optional database for the audit log")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *specPath == "" || *xrayBin == "" {
		usage()
		return 2
	}
	raw, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read spec: %v\n", err)
		return 1
	}
	var spec xray.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "spec: %v\n", err)
		return 1
	}
	endpoints, err := spec.Endpoints()
	if err != nil {
		fmt.Fprintf(os.Stderr, "spec: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	failed := false
	for _, ep := range endpoints {
		if ep.Listen == "" {
			ep.Listen = "127.0.0.1"
		}
		rep, err := (xray.Canary{Bin: *xrayBin, Endpoint: ep}).Run(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s\n%v\n%s\n", ep.Inbound.Remark, err, rep.Detail)
			logEvent(*dbPath, "error", "canary", "FAIL "+ep.Inbound.Remark, err.Error())
			failed = true
			continue
		}
		fmt.Printf("OK %s\n%s\n", ep.Inbound.Remark, rep.Link)
		logEvent(*dbPath, "info", "canary", "OK "+ep.Inbound.Remark, rep.Link)
	}
	if failed {
		return 1
	}
	return 0
}

func guardCmd(args []string) int {
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	specPath := fs.String("spec", "", "inbound spec JSON")
	dialHost := fs.String("dial", "127.0.0.1", "address to check; not the public host")
	dbPath := fs.String("db", "", "optional database for the alert log")
	repair := fs.Bool("repair", false, "if a port is closed, restart the core from this spec once")
	xrayBin := fs.String("xray", os.Getenv("XRAY_BIN"), "xray binary, required with -repair")
	configPath := fs.String("config", "", "config path written by -repair")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *specPath == "" || (*repair && (*xrayBin == "" || *configPath == "")) {
		usage()
		return 2
	}
	raw, err := os.ReadFile(*specPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read spec: %v\n", err)
		return 1
	}
	var spec xray.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintf(os.Stderr, "spec: %v\n", err)
		return 1
	}
	endpoints, err := spec.Endpoints()
	if err != nil {
		fmt.Fprintf(os.Stderr, "spec: %v\n", err)
		return 1
	}
	var alert guard.Alerter
	if *dbPath != "" {
		st, err := store.Open(*dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "db: %v\n", err)
			return 1
		}
		defer st.Close()
		alert = st
	}
	var restart func(context.Context) error
	if *repair {
		sup := &xray.Supervisor{Bin: *xrayBin, ConfigPath: *configPath, Runner: xray.ExecRunner{}}
		var once sync.Once
		var restartErr error
		restart = func(ctx context.Context) error {
			once.Do(func() {
				cfg, err := xray.Build(endpoints)
				if err != nil {
					restartErr = err
					return
				}
				restartErr = sup.Apply(ctx, cfg)
			})
			return restartErr
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	failed := false
	for _, ep := range endpoints {
		rep, err := (guard.Watcher{
			Endpoint: ep,
			DialHost: *dialHost,
			Restart:  restart,
			Alert:    alert,
			Timeout:  3 * time.Second,
		}).Run(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL %s\n%v\n", ep.Inbound.Remark, err)
			failed = true
			continue
		}
		state := "OK"
		if !rep.OK {
			state = "FAIL"
			failed = true
		}
		if rep.Repaired {
			state += " repaired"
		}
		fmt.Printf("%s %s\n", state, ep.Inbound.Remark)
		for _, item := range rep.Items {
			mark := "ok"
			if !item.OK {
				mark = "BAD"
			}
			fmt.Printf("  %s %s: %s\n", mark, item.Name, item.Detail)
		}
	}
	if failed {
		return 1
	}
	return 0
}

func pinCmd(args []string) int {
	fs := flag.NewFlagSet("pin", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory with the installed core")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Printf("tested %s\n", xray.PinnedVersion)
	if *dir == "" {
		return 0
	}
	pin, err := xray.ReadPin(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "installed pin: %v\n", err)
		return 1
	}
	fmt.Printf("installed %s %s\n", pin.Version, pin.SHA256)
	return 0
}

func upgradeCmd(args []string) int {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory that holds the core")
	binPath := fs.String("bin", "", "candidate xray binary")
	version := fs.String("version", "", "version string the candidate must print")
	dbPath := fs.String("db", "", "optional database for the audit log")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" || *binPath == "" || *version == "" {
		usage()
		return 2
	}
	candidate, err := os.ReadFile(*binPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read bin: %v\n", err)
		return 1
	}
	rolled, err := xray.Stage(*dir, candidate, *version, func(path string) error {
		out, runErr := exec.Command(path, "version").CombinedOutput()
		if runErr != nil {
			return fmt.Errorf("%w: %s", runErr, out)
		}
		if !bytesContains(out, []byte(*version)) {
			return fmt.Errorf("binary did not report %s", *version)
		}
		return nil
	})
	if err != nil {
		if rolled {
			fmt.Fprintf(os.Stderr, "ROLLED BACK\n%v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "upgrade: %v\n", err)
		}
		logEvent(*dbPath, "error", "upgrade", "upgrade to "+*version+" failed", err.Error())
		return 1
	}
	fmt.Printf("installed %s\n", *version)
	logEvent(*dbPath, "info", "upgrade", "installed "+*version, "")
	return 0
}

func bytesContains(b, sub []byte) bool {
	return len(sub) > 0 && len(b) >= len(sub) && indexBytes(b, sub) >= 0
}

// logEvent records an action in the shared audit log. It never fails the
// command itself: the action already happened, the log is best-effort —
// like guard, which also ignores logging errors.
func logEvent(dbPath, level, actor, message, meta string) {
	if dbPath == "" {
		return
	}
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		return
	}
	defer st.Close()
	if err := st.AddEvent(level, actor, message, meta); err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
	}
}

func indexBytes(b, sub []byte) int {
	for i := 0; i+len(sub) <= len(b); i++ {
		ok := true
		for j := range sub {
			if b[i+j] != sub[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func backupCmd(args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	dbPath := fs.String("db", "", "panel database")
	outPath := fs.String("out", "", "tar.gz to write")
	xrayDir := fs.String("xray-dir", "", "directory with PIN")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" || *outPath == "" {
		usage()
		return 2
	}
	if err := backup.Backup(*dbPath, *xrayDir, *outPath); err != nil {
		fmt.Fprintf(os.Stderr, "backup: %v\n", err)
		return 1
	}
	fmt.Printf("backup %s\n", *outPath)
	logEvent(*dbPath, "info", "backup", "backup "+*outPath, "")
	return 0
}

func restoreCmd(args []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	inPath := fs.String("in", "", "tar.gz to read")
	dbPath := fs.String("db", "", "panel database to write")
	xrayDir := fs.String("xray-dir", "", "directory with PIN")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *inPath == "" || *dbPath == "" {
		usage()
		return 2
	}
	if err := backup.Restore(*inPath, *dbPath, *xrayDir); err != nil {
		fmt.Fprintf(os.Stderr, "restore: %v\n", err)
		return 1
	}
	fmt.Printf("restored %s -> %s\n", *inPath, *dbPath)
	logEvent(*dbPath, "info", "restore", "restored from "+*inPath, "")
	return 0
}
