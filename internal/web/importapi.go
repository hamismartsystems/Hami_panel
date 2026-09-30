package web

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hamismartsystems/hami_panel/internal/importer"
	"github.com/hamismartsystems/hami_panel/internal/store"
)

/* ── sessions ────────────────────────────────────────────────────────── */

// An import happens in two steps: look, then commit. The file being looked
// at has to survive between the two requests, and the apply must read that
// same file rather than trust anything the browser sends back — otherwise
// what the operator approved and what gets written could differ.
type importSession struct {
	path     string // the database to read
	tempDir  string // non-empty when we own the file and must delete it
	xray     string // marzban's xray_config.json, when supplied
	created  time.Time
	original string // what to show the operator
}

type importSessions struct {
	mu   sync.Mutex
	byID map[string]*importSession
}

const importSessionTTL = 30 * time.Minute

func (s *importSessions) put(sess *importSession) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byID == nil {
		s.byID = map[string]*importSession{}
	}
	s.sweepLocked()
	id := randomToken()
	s.byID[id] = sess
	return id
}

func (s *importSessions) get(id string) (*importSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked()
	sess, ok := s.byID[id]
	return sess, ok
}

func (s *importSessions) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.byID[id]; ok {
		sess.cleanup()
		delete(s.byID, id)
	}
}

// sweepLocked removes uploads nobody came back for, so an abandoned wizard
// does not leave a copy of somebody's panel on disk forever.
func (s *importSessions) sweepLocked() {
	cutoff := time.Now().Add(-importSessionTTL)
	for id, sess := range s.byID {
		if sess.created.Before(cutoff) {
			sess.cleanup()
			delete(s.byID, id)
		}
	}
}

func (sess *importSession) cleanup() {
	if sess.tempDir != "" {
		_ = os.RemoveAll(sess.tempDir)
	}
}

func randomToken() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

/* ── request plumbing ────────────────────────────────────────────────── */

const maxUploadBytes = 128 << 20 // a panel database is small; this is generous

type importParams struct {
	host     string
	nodeName string
	flow     string
	keepSubs bool
}

func importParamsFrom(r *http.Request) importParams {
	return importParams{
		host:     strings.TrimSpace(r.FormValue("host")),
		nodeName: strings.TrimSpace(r.FormValue("node")),
		flow:     strings.TrimSpace(r.FormValue("flow")),
		keepSubs: truthy(r.FormValue("keep_subs")),
	}
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func (s *Server) importOptions(p importParams) (importer.Options, error) {
	opt := importer.Options{Host: p.host, Flow: p.flow, KeepSubTokens: p.keepSubs}
	if p.nodeName != "" {
		n, err := s.Store.GetNodeByName(p.nodeName)
		if err != nil {
			return opt, fmt.Errorf("node %q: %w", p.nodeName, err)
		}
		opt.NodeID = n.ID
	}
	return opt, nil
}

// newImportSession accepts either an uploaded file or a path on this
// server. An uploaded file is written to a private temp directory; a path
// is used where it lies, and never copied into the workspace.
func newImportSession(r *http.Request) (*importSession, error) {
	if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		return nil, fmt.Errorf("could not read the upload: %w", err)
	}

	file, header, err := r.FormFile("db")
	if err == nil {
		defer file.Close()
		if header.Size > maxUploadBytes {
			return nil, fmt.Errorf("that file is %d MB; the limit is %d MB",
				header.Size>>20, maxUploadBytes>>20)
		}
		dir, err := os.MkdirTemp("", "hami-upload-*")
		if err != nil {
			return nil, err
		}
		sess := &importSession{
			tempDir: dir, created: time.Now(),
			original: filepath.Base(header.Filename),
		}
		sess.path = filepath.Join(dir, "source.db")
		if err := writeUpload(sess.path, file); err != nil {
			sess.cleanup()
			return nil, err
		}
		// Marzban keeps its inbound definitions outside the database.
		if xf, xh, err := r.FormFile("xray"); err == nil {
			defer xf.Close()
			if xh.Size <= maxUploadBytes {
				sess.xray = filepath.Join(dir, "xray_config.json")
				if err := writeUpload(sess.xray, xf); err != nil {
					sess.cleanup()
					return nil, err
				}
			}
		}
		return sess, nil
	}

	path := strings.TrimSpace(r.FormValue("path"))
	if path == "" {
		return nil, errors.New("choose a file to upload, or give the path to one on this server")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &importSession{
		path: path, created: time.Now(), original: path,
		xray: strings.TrimSpace(r.FormValue("xray_path")),
	}, nil
}

func writeUpload(dst string, src io.Reader) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(src, maxUploadBytes)); err != nil {
		return err
	}
	return out.Sync()
}

// readSource opens the panel behind a session and reads it. The importer
// copies the file and opens the copy read-only, so nothing here can write
// to the operator's other panel.
func readSource(sess *importSession, opt importer.Options) (*importer.Snapshot, string, error) {
	if sess.xray != "" {
		cfg, err := importer.LoadXrayConfig(sess.xray)
		if err != nil {
			return nil, "", err
		}
		opt.XrayConfig = cfg
	}
	src, err := importer.Open(sess.path, "")
	if err != nil {
		return nil, "", err
	}
	defer src.Close()
	snap, err := src.Read(opt)
	if err != nil {
		return nil, "", err
	}
	return snap, src.Digest, nil
}

/* ── endpoints ───────────────────────────────────────────────────────── */

// apiImportPreview reads the other panel and reports exactly what would
// happen. It writes nothing.
func (s *Server) apiImportPreview(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	sess, err := newImportSession(r)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	opt, err := s.importOptions(importParamsFrom(r))
	if err != nil {
		sess.cleanup()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	snap, digest, err := readSource(sess, opt)
	if err != nil {
		sess.cleanup()
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	plan, err := importer.BuildPlan(s.Store, snap)
	if err != nil {
		sess.cleanup()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	id := s.imports.put(sess)
	writeJSON(w, http.StatusOK, previewBody(id, sess, snap, plan, digest))
}

func previewBody(id string, sess *importSession, snap *importer.Snapshot,
	plan *importer.Plan, digest string,
) map[string]any {
	newIn, newCl, oldIn, oldCl := plan.Counts()

	inbounds := make([]map[string]any, 0, len(snap.Inbounds))
	for _, in := range snap.Inbounds {
		inbounds = append(inbounds, map[string]any{
			"remark": in.Inbound.Remark, "port": in.Inbound.Port,
			"protocol": in.Inbound.Protocol, "security": in.Inbound.Security,
			"transport": in.Inbound.Transport, "host": in.Inbound.Host,
			"flow": in.Inbound.Flow, "clients": len(in.Clients),
		})
	}
	skipped := make([]map[string]string, 0, len(snap.Skipped))
	for _, k := range snap.Skipped {
		skipped = append(skipped, map[string]string{
			"what": k.What, "ref": k.Ref, "reason": k.Reason,
		})
	}
	warnings := snap.Warnings
	if warnings == nil {
		warnings = []string{} // an empty list, never null, so callers can just iterate
	}
	conflicts := plan.Conflicts
	if conflicts == nil {
		conflicts = []string{}
	}
	// Rows that already exist, so the operator can see nothing is about to
	// be duplicated.
	existing := make([]string, 0)
	for _, pi := range plan.Inbounds {
		if pi.Action == importer.Exists {
			existing = append(existing, pi.Inbound.Remark)
		}
	}

	return map[string]any{
		"session":  id,
		"source":   sess.original,
		"panel":    string(snap.Kind),
		"variant":  snap.Variant,
		"checksum": digest[:16],
		"inbounds": inbounds,
		"warnings": warnings,
		"skipped":  skipped,
		"plan": map[string]any{
			"new_inbounds":      newIn,
			"new_clients":       newCl,
			"existing_inbounds": oldIn,
			"existing_clients":  oldCl,
			"existing_names":    existing,
			"conflicts":         conflicts,
		},
	}
}

// apiImportApply commits a previewed import. It re-reads the source and
// rebuilds the plan rather than trusting the browser, then refuses if the
// shape of the work changed since the preview.
func (s *Server) apiImportApply(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	if err := r.ParseMultipartForm(1 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		_ = r.ParseForm()
	}
	id := strings.TrimSpace(r.FormValue("session"))
	sess, ok := s.imports.get(id)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "this preview has expired — take another look and try again"})
		return
	}

	opt, err := s.importOptions(importParamsFrom(r))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	snap, _, err := readSource(sess, opt)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	plan, err := importer.BuildPlan(s.Store, snap)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(plan.Conflicts) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "there are conflicts to settle first",
			"conflicts": plan.Conflicts,
		})
		return
	}

	// The operator approved a specific amount of work. If the source moved
	// under us, stop and make them look again.
	newIn, newCl, _, _ := plan.Counts()
	if want := r.FormValue("expect_clients"); want != "" {
		if n, err := strconv.Atoi(want); err == nil && n != newCl {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": fmt.Sprintf("the source changed since the preview: it now has %d "+
					"new clients instead of %d — take another look", newCl, n),
			})
			return
		}
	}
	if newIn == 0 && newCl == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "created_inbounds": 0, "created_clients": 0,
			"note": "everything in that panel is already here",
		})
		return
	}

	res, err := importer.Apply(s.Store, s.DBPath, plan)
	body := map[string]any{}
	if res != nil {
		body["backup"] = res.BackupPath
		body["created_inbounds"] = res.CreatedInbounds
		body["created_clients"] = res.CreatedClients
		body["verified"] = res.Verified
	}
	if err != nil {
		body["error"] = err.Error()
		_ = s.Store.AddEvent("error", a.Username, "import failed: "+err.Error(), "via=hp-ui")
		writeJSON(w, http.StatusInternalServerError, body)
		return
	}

	body["ok"] = true
	_ = s.Store.AddEvent("info", a.Username, fmt.Sprintf(
		"imported %d inbounds and %d clients from %s", res.CreatedInbounds,
		res.CreatedClients, snap.Kind), "via=hp-ui")
	s.imports.drop(id)
	writeJSON(w, http.StatusOK, body)
}

// apiImportCancel throws away an upload the operator decided against.
func (s *Server) apiImportCancel(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	// FormValue parses a multipart or urlencoded body on its own; calling
	// ParseForm first would leave the multipart fields unread.
	s.imports.drop(strings.TrimSpace(r.FormValue("session")))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

/* ── finding panels without being told where they are ────────────────── */

// candidatePaths are where the panels in the wild actually put their
// database. Asking an operator to type a path is asking them to know
// something they have no reason to know.
var candidatePaths = []string{
	"/etc/x-ui/x-ui.db",
	"/usr/local/x-ui/x-ui.db",
	"/var/lib/x-ui/x-ui.db",
	"/etc/3x-ui/x-ui.db",
	"/var/lib/marzban/db.sqlite3",
	"/opt/marzban/db.sqlite3",
	"/var/lib/marzneshin/db.sqlite3",
}

// apiImportCandidates looks in the usual places, plus alongside this
// panel's own database, and reports every file that turns out to be a
// panel it can read. Anything it cannot read is simply not offered.
func (s *Server) apiImportCandidates(w http.ResponseWriter, r *http.Request, a *store.Admin) {
	seen := map[string]bool{}
	paths := append([]string{}, candidatePaths...)

	// Operators often copy the old panel's file next to the new one.
	if s.DBPath != "" {
		dir := filepath.Dir(s.DBPath)
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				full := filepath.Join(dir, name)
				if full == s.DBPath || strings.Contains(name, ".before-import-") {
					continue
				}
				switch strings.ToLower(filepath.Ext(name)) {
				case ".db", ".sqlite", ".sqlite3":
					paths = append(paths, full)
				}
			}
		}
	}

	found := make([]map[string]any, 0)
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		info, err := os.Stat(p)
		if err != nil || info.IsDir() {
			continue
		}
		src, err := importer.Open(p, "")
		if err != nil {
			continue // not a panel we know; say nothing rather than confuse
		}
		snap, rerr := src.Read(importer.Options{})
		entry := map[string]any{
			"path": p, "panel": string(src.Source.Kind()), "variant": src.Variant,
			"size": info.Size(),
		}
		if rerr == nil {
			clients := 0
			for _, in := range snap.Inbounds {
				clients += len(in.Clients)
			}
			entry["inbounds"] = len(snap.Inbounds)
			entry["clients"] = clients
		}
		src.Close()
		found = append(found, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": found})
}
