// Command hami generates an Xray config and the matching client links from
// one spec. Each link is built only from the inbound that client is on.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

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
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: hami gen -spec inbounds.json -out config.json\n")
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
