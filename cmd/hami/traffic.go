package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/traffic"
	"github.com/hamismartsystems/hami_panel/internal/xray"
)

// trafficCmd pulls usage out of the running core and into the panel:
//
//	hami traffic collect -db panel.db -xray /path/to/xray [-api 127.0.0.1:10085]
//
// Meant to run from a timer, once a minute. Each run asks for the
// counters and resets them, so the totals in the database are the sum of
// every run rather than whatever the core happens to hold right now.
func trafficCmd(args []string) int {
	if len(args) < 1 || args[0] != "collect" {
		fmt.Fprintln(os.Stderr,
			"usage:\n  hami traffic collect -db panel.db -xray /path/to/xray [-api 127.0.0.1:10085]")
		return 2
	}
	fs := flag.NewFlagSet("traffic collect", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	xrayBin := fs.String("xray", os.Getenv("XRAY_BIN"), "")
	api := fs.String("api", fmt.Sprintf("127.0.0.1:%d", xray.StatsAPIPort), "")
	quiet := fs.Bool("quiet", false, "only report problems")
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
		fmt.Println()
	}
	return 0
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
