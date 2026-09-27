// Command hami generates an Xray config and the matching client links from
// one spec. Each link is built only from the inbound that client is on.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

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
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage:\n  hami gen -spec inbounds.json -out config.json\n  hami canary -spec inbounds.json [-xray /path/to/xray]\n")
}

func gen(args []string) int {
	fs := flag.NewFlagSet("gen", flag.ContinueOnError)
	specPath := fs.String("spec", "", "inbound spec JSON")
	outPath := fs.String("out", "", "xray config to write")
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

func canary(args []string) int {
	fs := flag.NewFlagSet("canary", flag.ContinueOnError)
	specPath := fs.String("spec", "", "inbound spec JSON")
	xrayBin := fs.String("xray", os.Getenv("XRAY_BIN"), "path to the xray binary")
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
			failed = true
			continue
		}
		fmt.Printf("OK %s\n%s\n", ep.Inbound.Remark, rep.Link)
	}
	if failed {
		return 1
	}
	return 0
}
