package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/hamismartsystems/hami_panel/internal/store"
	"github.com/hamismartsystems/hami_panel/internal/subs"
	"github.com/hamismartsystems/hami_panel/internal/web"
)

func webCmd(args []string) int {
	if len(args) < 1 {
		webUsage()
		return 2
	}
	switch args[0] {
	case "serve":
		return webServe(args[1:])
	}
	webUsage()
	return 2
}

func webUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami web serve -db panel.db -addr :8080 [-base-url https://panel.example.com]

Serves:
  /hp-ui/login   HP-UI login page (HAMI PANEL, colors #9DC183/#0B6623/#043927)
  /hp-ui/        HP-UI dashboard: inbounds, users, nodes, logs (sign-in required)
  /sub/{token}   subscription (v2ray/Clash/sing-box)
  /sub/{token}/status  status + QR

Create the first operator with:
  hami admin create -db panel.db -user NAME
`)
}

func webServe(args []string) int {
	fs := flag.NewFlagSet("web serve", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	addr := fs.String("addr", ":8080", "")
	baseURL := fs.String("base-url", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil || *dbPath == "" {
		webUsage()
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		return 1
	}
	defer st.Close()

	subSrv := &subs.Server{Store: st, BaseURL: *baseURL}
	webHandler := web.HandlerWithDB(st, *dbPath)

	mux := http.NewServeMux()
	mux.Handle("/hp-ui/", webHandler)
	mux.Handle("/api/", webHandler)
	mux.Handle("/sub/", subSrv.Handler())
	mux.Handle("/", webHandler) // / -> dashboard when signed in, else login

	if n, err := st.CountAdmins(); err == nil && n == 0 {
		fmt.Fprintln(os.Stderr,
			"warning: no admin account yet — run: hami admin create -db "+*dbPath+" -user NAME")
	}
	fmt.Printf("HP-UI on %s\n", *addr)
	fmt.Printf("  login: http://%s/hp-ui/login\n", *addr)
	if *baseURL != "" {
		fmt.Printf("  sub: %s/sub/<token>\n", *baseURL)
	}
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}
