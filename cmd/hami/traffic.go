package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/apply"
	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/traffic"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

// trafficCmd pulls usage out of the running core and into the panel:
//
//	hami traffic collect -db panel.db -xray /path/to/xray [-api 127.0.0.1:10085]
//	   [-config /etc/hami/xray.json -reload-cmd "systemctl restart hami-xray"]
//
// Meant to run from a timer, once a minute. Each run asks for the
// counters and resets them, so the totals in the database are the sum of
// every run rather than whatever the core happens to hold right now.
func trafficCmd(args []string) int {
	if len(args) < 1 || args[0] != "collect" {
		fmt.Fprintln(os.Stderr,
			"usage:\n  hami traffic collect -db panel.db -xray /path/to/xray [-api 127.0.0.1:10085]\n"+
				"                        [-config /etc/hami/xray.json -reload-cmd \"systemctl restart hami-xray\"]\n\n"+
				"  -config and -reload-cmd are what make an exhausted quota or an ended term\n"+
				"  take effect on the running core; without them the panel is only counting.")
		return 2
	}
	fs := flag.NewFlagSet("traffic collect", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	xrayBin := fs.String("xray", os.Getenv("XRAY_BIN"), "")
	api := fs.String("api", fmt.Sprintf("127.0.0.1:%d", xray.StatsAPIPort), "")
	quiet := fs.Bool("quiet", false, "only report problems")
	configPath := fs.String("config", "",
		"where to write the core's rebuilt config; with -reload-cmd this is "+
			"what makes a switch-off actually take effect")
	reloadCmd := fs.String("reload-cmd", "",
		"command that makes the core read the rebuilt config; run only when this "+
			"round switched somebody off, so a normal minute never drops connections")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *dbPath == "" || *xrayBin == "" {
		fmt.Fprintln(os.Stderr, "traffic collect: -db and -xray are required")
		return 2
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	samples, err := (traffic.Collector{Bin: *xrayBin, API: *api}).Query(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "collect: %v\n", err)
		_ = st.AddEvent("warn", "traffic", "could not read the core's statistics", err.Error())
		return 1
	}

	res, err := traffic.Apply(st, samples, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		return 1
	}
	for _, e := range res.Disabled {
		_ = st.AddEvent("warn", "traffic", "disabled "+e+": quota used up", "")
	}
	for _, e := range res.Expired {
		_ = st.AddEvent("warn", "traffic", "disabled "+e+": term ended", "")
	}
	// Switching a customer off only takes effect once the core has read a
	// config that no longer lists them — and this process is the one that
	// knows a customer was switched off, so this process has to rebuild
	// the config. Restarting the core on top of the panel's older file
	// would bring the switched-off customer straight back.
	//
	// It runs on the rounds that changed something and stays out of the
	// way the rest of the time: a restart every minute would drop every
	// live connection every minute.
	if len(res.Disabled)+len(res.Expired) > 0 && (*configPath != "" || *reloadCmd != "") {
		if err := pushToCore(st, *configPath, *reloadCmd); err != nil {
			fmt.Fprintf(os.Stderr, "core: %v\n", err)
			_ = st.AddEvent("error", "traffic",
				"the core was not told that customers were switched off", err.Error())
			return 1
		}
	}
	if len(res.Unknown) > 0 {
		_ = st.AddEvent("warn", "traffic",
			fmt.Sprintf("the core reported %d account(s) the panel does not know",
				len(res.Unknown)), fmt.Sprint(res.Unknown))
	}
	if !*quiet {
		fmt.Printf("counted %d account(s), %s in, %s out, %d online",
			res.Matched, human(res.AddedUp), human(res.AddedDn), res.Online)
		if len(res.Disabled) > 0 {
			fmt.Printf(", disabled %v on quota", res.Disabled)
		}
		if len(res.Expired) > 0 {
			fmt.Printf(", disabled %v at end of term", res.Expired)
		}
		fmt.Println()
	}
	return 0
}

// pushToCore rebuilds the core's config from the database and tells the
// core to read it. With only a reload command and no path it just runs
// the command, for an operator who generates the config elsewhere.
func pushToCore(st *store.Store, configPath, reloadCmd string) error {
	if configPath == "" {
		return runReload(reloadCmd, 30*time.Second)
	}
	applier := &apply.Applier{Store: st, ConfigPath: configPath}
	if reloadCmd != "" {
		applier.Reload = strings.Fields(reloadCmd)
	}
	return applier.Now()
}

// runReload runs the reload command through the shell, because that is
// how an operator writes it in a unit file ("systemctl restart x" works
// either way, but "systemctl restart $UNIT" does not).
func runReload(cmdline string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", cmdline).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func human(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for v := n / u; v >= u; v /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
