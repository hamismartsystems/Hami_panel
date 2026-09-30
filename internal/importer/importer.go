// Package importer brings inbounds and clients over from another panel.
//
// It is part of HAMI Panel, not a side tool: the same code backs the
// `hami import` command and, later, the dashboard. That matters because a
// migration is the first thing a new operator does, and a migration that
// needs a second program is a migration people put off.
//
// The one rule everything here is built around: the source panel must come
// out of this completely untouched. Concretely
//
//   - the source file is never opened read-write. It is copied first with
//     SQLite's own backup API where possible, and the copy is opened with
//     query_only set, so even a bug cannot issue a write;
//   - the source is checksummed before and after, and the import fails
//     loudly if the digest moved;
//   - nothing is written to HAMI's database unless Apply is called, and
//     Apply runs inside one transaction that is rolled back on any error;
//   - Apply is idempotent. Running it twice imports nothing the second
//     time, so an interrupted migration can simply be repeated.
//
// Speed is deliberately not the goal. Every record is validated, and after
// writing, Apply reads everything back and compares it against the plan.
package importer

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/store"
)

// Kind identifies a source panel.
type Kind string

const (
	Kind3xUI    Kind = "3x-ui"          // MHSanaei/3x-ui
	KindAlireza Kind = "x-ui (alireza)" // alireza0/x-ui
	KindXUI     Kind = "x-ui (legacy)"  // vaxilu/x-ui
	KindMarzban Kind = "marzban"        // Gozargah/marzban
)

// Source reads one panel family.
type Source interface {
	Kind() Kind
	// Detect reports whether an already-open database looks like this
	// panel, and if so describes the variant it found.
	Detect(db *sql.DB) (bool, string)
	// Read translates the source into HAMI's own shape. It must never
	// write to db.
	Read(db *sql.DB, opt Options) (*Snapshot, error)
}

// sources are tried in order; the first that recognises the database wins.
// 3x-ui is checked before the older forks because its schema is a superset
// of theirs.
func sources() []Source {
	return []Source{
		&xuiSource{kind: Kind3xUI},
		&marzbanSource{},
	}
}

// Options tune a read.
type Options struct {
	// Host overrides the address clients will connect to. Empty means
	// "use whatever the source panel advertises", which is usually right.
	Host string
	// NodeID attaches every imported inbound to a node. 0 is this server.
	NodeID int64
	// Flow forces the inbound flow instead of taking the one most clients
	// use. HAMI stores flow once per inbound, so when a source panel mixes
	// them the operator needs a way to decide.
	Flow string
	// KeepSubTokens preserves each client's subscription id. When the new
	// panel is reachable at the same address, existing customer links keep
	// working and nobody has to be told to re-add anything.
	KeepSubTokens bool
	// XrayConfig carries marzban's inbound definitions, which live in a
	// file rather than its database. Left nil, the reader looks for
	// xray_config.json next to the database, which is where marzban puts
	// it; that is one less thing for an operator to know.
	XrayConfig *XrayConfig
	// sourceDir is where the source database sits, filled in by Read.
	sourceDir string
	// Now is injected by tests.
	Now func() time.Time
}

func (o Options) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now().UTC()
}

// Snapshot is a source panel translated into HAMI's own types. Nothing in
// here has been written anywhere yet.
type Snapshot struct {
	Kind     Kind
	Variant  string
	Inbounds []Imported
	// Skipped records things deliberately left behind, with the reason.
	// A migration that silently drops data is worse than one that fails.
	Skipped []Skip
	// Warnings are things that were imported but deserve a second look.
	Warnings []string
}

// Imported is one inbound plus everything that belongs to it.
type Imported struct {
	Inbound store.Inbound
	Secret  store.InboundSecret
	Clients []store.Client
	// SourceRef identifies the row it came from, for the report.
	SourceRef string
}

// Skip is one thing that was not imported, and why.
type Skip struct {
	What   string // "inbound" or "client"
	Ref    string
	Reason string
}

func (s *Snapshot) clientCount() int {
	n := 0
	for _, in := range s.Inbounds {
		n += len(in.Clients)
	}
	return n
}

// Summary renders counts for the CLI and the dashboard.
func (s *Snapshot) Summary() string {
	return fmt.Sprintf("%s (%s): %d inbounds, %d clients, %d skipped",
		s.Kind, s.Variant, len(s.Inbounds), s.clientCount(), len(s.Skipped))
}

/* ── safe access to the source ───────────────────────────────────────── */

// Opened is a read-only handle on a copy of the source database, plus the
// evidence that the original was not disturbed.
type Opened struct {
	DB       *sql.DB
	Source   Source
	Variant  string
	Original string
	Copy     string
	Digest   string

	cleanup func()
}

// Close releases the copy.
func (o *Opened) Close() error {
	var err error
	if o.DB != nil {
		err = o.DB.Close()
	}
	if o.cleanup != nil {
		o.cleanup()
	}
	return err
}

// VerifyUntouched re-hashes the original file and fails if it changed
// while we were reading. It is cheap, and it turns "we promise we did not
// write to your panel" into something checkable.
func (o *Opened) VerifyUntouched() error {
	after, err := fileDigest(o.Original)
	if err != nil {
		return fmt.Errorf("re-reading the source: %w", err)
	}
	if after != o.Digest {
		return fmt.Errorf("the source database changed while it was being read "+
			"(was %s, now %s) — nothing was imported; take the panel offline and retry",
			short(o.Digest), short(after))
	}
	return nil
}

// Open copies the source database, opens the copy read-only, and works out
// which panel it belongs to. force may be empty for auto-detection.
func Open(path string, force Kind) (*Opened, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("source database: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source database: %s is a directory", path)
	}

	digest, err := fileDigest(path)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "hami-import-*")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	copyPath := filepath.Join(tmp, "source.db")

	if err := copyDatabase(path, copyPath); err != nil {
		cleanup()
		return nil, err
	}

	// query_only makes a write impossible rather than merely unintended.
	dsn := "file:" + copyPath + "?_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		cleanup()
		return nil, fmt.Errorf("opening the copy: %w", err)
	}

	o := &Opened{DB: db, Original: path, Copy: copyPath, Digest: digest, cleanup: cleanup}

	for _, src := range sources() {
		if force != "" && src.Kind() != force {
			continue
		}
		ok, variant := src.Detect(db)
		if ok {
			o.Source, o.Variant = src, variant
			return o, nil
		}
	}
	db.Close()
	cleanup()
	if force != "" {
		return nil, fmt.Errorf("this database does not look like %s", force)
	}
	return nil, errors.New("unrecognised panel: expected a 3x-ui, x-ui or marzban database")
}

// Read produces the snapshot.
func (o *Opened) Read(opt Options) (*Snapshot, error) {
	opt.sourceDir = filepath.Dir(o.Original)
	snap, err := o.Source.Read(o.DB, opt)
	if err != nil {
		return nil, err
	}
	if err := o.VerifyUntouched(); err != nil {
		return nil, err
	}
	snap.Kind, snap.Variant = o.Source.Kind(), o.Variant
	return snap, nil
}

// copyDatabase prefers SQLite's backup API so a live, busy panel still
// yields a consistent copy. It falls back to a plain file copy only when
// the source cannot be opened, which happens with locked or exotic files.
func copyDatabase(src, dst string) error {
	db, err := sql.Open("sqlite", "file:"+src+"?_pragma=query_only(1)&mode=ro")
	if err == nil {
		defer db.Close()
		if err = db.Ping(); err == nil {
			// VACUUM INTO writes a fresh, consistent database without
			// touching the original.
			if _, err = db.Exec("VACUUM INTO ?", dst); err == nil {
				return nil
			}
		}
	}
	return rawCopy(src, dst)
}

func rawCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func short(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

/* ── helpers shared by the adapters ──────────────────────────────────── */

// tableExists is how every adapter recognises its own schema.
func tableExists(db *sql.DB, name string) bool {
	var n int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type IN ('table','view') AND name = ?`,
		name).Scan(&n)
	return err == nil && n > 0
}

func columnExists(db *sql.DB, table, column string) bool {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil && strings.EqualFold(n, column) {
			return true
		}
	}
	return false
}

// msToTime converts a panel timestamp. Panels in this family store
// milliseconds since the epoch, use 0 for "never", and a negative value
// for "the countdown has not started yet" — that last case is preserved as
// no expiry plus a warning, because inventing a date would quietly shorten
// somebody's subscription.
func msToTime(ms int64) (*time.Time, bool) {
	switch {
	case ms == 0:
		return nil, false
	case ms < 0:
		return nil, true
	default:
		t := time.UnixMilli(ms).UTC()
		return &t, false
	}
}

// sortInbounds keeps output deterministic, which makes the plan diffable
// and the tests meaningful.
func sortInbounds(list []Imported) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Inbound.Port != list[j].Inbound.Port {
			return list[i].Inbound.Port < list[j].Inbound.Port
		}
		return list[i].Inbound.Remark < list[j].Inbound.Remark
	})
	for i := range list {
		c := list[i].Clients
		sort.SliceStable(c, func(a, b int) bool { return c[a].Email < c[b].Email })
	}
}
