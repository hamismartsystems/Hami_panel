package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/agent"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

// nodeCmd manages remote nodes:
//
//	hami node add -db panel.db -name fr-1 -address 1.2.3.4:9100
//	hami node list -db panel.db
//	hami node check -db panel.db [-id N]
//	hami node remove -db panel.db -id N
//
// hami agent run -listen 127.0.0.1:9100 -name fr-1 -api-key KEY
func nodeCmd(args []string) int {
	if len(args) < 1 {
		nodeUsage()
		return 2
	}
	switch args[0] {
	case "add":
		return nodeAdd(args[1:])
	case "list":
		return nodeList(args[1:])
	case "check":
		return nodeCheck(args[1:])
	case "remove":
		return nodeRemove(args[1:])
	}
	nodeUsage()
	return 2
}

func nodeUsage() {
	fmt.Fprintf(os.Stderr, `usage:
  hami node add -db panel.db -name NAME -address HOST:PORT [-api-key K]
  hami node list -db panel.db
  hami node check -db panel.db [-id N]
  hami node remove -db panel.db -id N
`)
}

func nodeAdd(args []string) int {
	fs := flag.NewFlagSet("node add", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	name := fs.String("name", "", "")
	address := fs.String("address", "", "")
	apiKey := fs.String("api-key", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	n := &store.Node{Name: *name, Address: *address, APIKey: *apiKey}
	if err := st.CreateNode(n); err != nil {
		fmt.Fprintf(os.Stderr, "create: %v\n", err)
		return 1
	}
	logEvent(*dbPath, "info", "node", fmt.Sprintf("registered %s at %s", n.Name, n.Address), "")
	fmt.Printf("✅ node %d registered\n   name: %s\n   address: %s\n   api key: %s\n", n.ID, n.Name, n.Address, n.APIKey)
	return 0
}

func nodeList(args []string) int {
	fs := flag.NewFlagSet("node list", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	nodes, err := st.ListNodes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		return 1
	}
	fmt.Printf("%-4s %-12s %-22s %-10s %s\n", "ID", "NAME", "ADDRESS", "STATUS", "LAST SEEN")
	for _, n := range nodes {
		seen := "—"
		if n.LastSeen != nil {
			seen = n.LastSeen.UTC().Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-4d %-12s %-22s %-10s %s\n", n.ID, n.Name, n.Address, n.Status, seen)
	}
	return 0
}

func nodeCheck(args []string) int {
	fs := flag.NewFlagSet("node check", flag.ContinueOnError)
	dbPath := fs.String("db", "", "")
	onlyID := fs.Int64("id", 0, "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	st, ok := openDBOr(*dbPath)
	if !ok {
		return 1
	}
	defer st.Close()
	nodes, err := st.ListNodes()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list: %v\n", err)
		return 1
	}
	fails := 0
	for _, n := range nodes {
		if *onlyID != 0 && n.ID != *onlyID {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		h, err := agent.ProbeHealth(ctx, n.Address, n.Name, n.APIKey)
		cancel()
		status := agent.StatusOf(h, err)
		now := time.Now().UTC()
		if mErr := st.MarkNodeStatus(n.ID, status, now); mErr != nil {
			fmt.Fprintf(os.Stderr, "mark %s: %v\n", n.Name, mErr)
			return 1
		}
		switch {
		case err != nil:
			fails++
			fmt.Printf("✗ %-12s down: %v\n", n.Name, err)
			logEvent(*dbPath, "warn", "node", fmt.Sprintf("%s probe failed: %v", n.Name, err), "")
		case status == "unstable":
			fmt.Printf("~ %-12s unstable: load=%.1f mem=%.0f%%\n", n.Name, h.Load1, h.MemUse*100)
			logEvent(*dbPath, "warn", "node", fmt.Sprintf("%s unstable: load=%.1f mem=%.2f", n.Name, h.Load1, h.MemUse), "")
		default:
			fmt.Printf("✓ %-12s healthy: load=%.1f mem=%.0f%%\n", n.Name, h.Load1, h.MemUse*100)
		}
	}
	if fails > 0 {
		return 1
	}
	return 0
}

func nodeRemove(args []string) int {
	fs := flag.NewFlagSet("node remove", flag.ContinueOnError)
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
	if err := st.DeleteNode(*id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "node %d not found\n", *id)
			return 1
		}
		fmt.Fprintf(os.Stderr, "remove: %v\n", err)
		return 1
	}
	logEvent(*dbPath, "warn", "node", fmt.Sprintf("removed node %d", *id), "")
	fmt.Printf("✅ removed node %d\n", *id)
	return 0
}

// agentCmd runs the node agent:
//
//	hami agent run -listen 127.0.0.1:9100 -name fr-1 -api-key KEY
func agentCmd(args []string) int {
	if len(args) < 1 || args[0] != "run" {
		fmt.Fprintf(os.Stderr, "usage: hami agent run -listen ADDR -name NAME -api-key KEY\n")
		return 2
	}
	fs := flag.NewFlagSet("agent run", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:9100", "")
	name := fs.String("name", "", "")
	apiKey := fs.String("api-key", "", "")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args[1:]); err != nil || *name == "" || *apiKey == "" {
		return 2
	}
	srv := &agent.Server{NodeName: *name, APIKey: *apiKey}
	fmt.Printf("agent %q on %s\n", *name, *listen)
	if err := http.ListenAndServe(*listen, srv.Handler()); err != nil {
		fmt.Fprintf(os.Stderr, "agent: %v\n", err)
		return 1
	}
	return 0
}
