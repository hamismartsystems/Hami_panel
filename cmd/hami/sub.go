package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
)

// subCmd serves subscription links and status pages:
//
//	hami sub serve -db panel.db [-addr :8080] [-base-url https://panel.example.com]
func subCmd(args []string) int {
	if len(args) < 1 || args[0] != "serve" {
		fmt.Fprintf(os.Stderr, "usage: hami sub serve -db panel.db [-addr :8080] [-base-url URL]\n")
		return 2
	}
	fs := flag.NewFlagSet("sub serve", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	addr := fs.String("addr", ":8080", "")
	baseURL := fs.String("base-url", "", "public base url for links/QR")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil || *dbPath == "" {
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	srv := &subs.Server{Store: st, BaseURL: *baseURL}
	fmt.Printf("sub server on %s\n", *addr)
	if *baseURL != "" {
		fmt.Printf("sub links look like %s/sub/<token>\n", *baseURL)
	}
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}
