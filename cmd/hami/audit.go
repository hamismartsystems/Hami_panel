// audit prints the panel's event log, newest first. The store already
// records the events (guard, canary, backup, restore, upgrade); this is
// how the admin actually reads them.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func auditCmd(args []string) int {
	fs := flag.NewFlagSet("audit", flag.ContinueOnError)
	dbPath := fs.String("db", "", "panel database")
	tail := fs.Int("tail", 50, "how many events to show")
	level := fs.String("level", "", "only this level (info, warn, error)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dbPath == "" {
		usage()
		return 2
	}
	st, err := store.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer st.Close()
	events, err := st.RecentEvents(*tail)
	if err != nil {
		fmt.Fprintf(os.Stderr, "audit: %v\n", err)
		return 1
	}
	for _, e := range events {
		if *level != "" && e.Level != *level {
			continue
		}
		fmt.Println(formatEvent(e))
	}
	return 0
}

// formatEvent renders one audit row: ts level actor message meta.
func formatEvent(e store.Event) string {
	s := fmt.Sprintf("%s  %-5s  %-8s  %s",
		e.TS.Format("2006-01-02 15:04:05"), e.Level, e.Actor, e.Message)
	if e.Meta != "" {
		s += "  " + e.Meta
	}
	return s
}
