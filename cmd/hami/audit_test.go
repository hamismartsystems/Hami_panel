package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

func TestFormatEvent(t *testing.T) {
	ts := time.Date(2026, 9, 28, 14, 30, 0, 0, time.UTC)
	e := store.Event{TS: ts, Level: "warn", Actor: "guard", Message: "port closed", Meta: "{}"}
	got := formatEvent(e)
	for _, want := range []string{"2026-09-28 14:30:00", "warn", "guard", "port closed", "{}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatEvent = %q, want it to contain %q", got, want)
		}
	}
}

func TestAuditCmdReadsEvents(t *testing.T) {
	db := filepath.Join(t.TempDir(), "panel.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvent("info", "canary", "OK reality-main", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEvent("error", "guard", "port 443 closed", ""); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code := auditCmd([]string{"-db", db, "-tail", "10"}); code != 0 {
		t.Fatalf("auditCmd returned %d, want 0", code)
	}
	if code := auditCmd([]string{"-db", db, "-level", "error"}); code != 0 {
		t.Fatalf("auditCmd -level error returned %d, want 0", code)
	}
}

func TestAuditCmdRequiresDB(t *testing.T) {
	if code := auditCmd([]string{}); code != 2 {
		t.Fatalf("auditCmd without -db returned %d, want 2", code)
	}
}

func TestLogEventWritesToDB(t *testing.T) {
	db := filepath.Join(t.TempDir(), "panel.db")
	logEvent(db, "info", "upgrade", "installed 26.3.27", "")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	events, err := st.RecentEvents(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Actor != "upgrade" {
		t.Fatalf("got %+v, want one upgrade event", events)
	}
}

func TestLogEventWithoutDBIsNoOp(t *testing.T) {
	logEvent("", "info", "backup", "must not panic", "")
}
