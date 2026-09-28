package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Manifest is stored inside the archive.
type Manifest struct {
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	HasDB     bool      `json:"has_db"`
	HasPIN    bool      `json:"has_pin"`
}

// Backup creates a tar.gz at outPath containing the panel database and
// the core PIN. The xray binary itself is not stored — only its version
// and hash from PIN, so a restore does not need to ship a large binary.
func Backup(dbPath, xrayDir, outPath string) error {
	if dbPath == "" {
		return fmt.Errorf("backup: db path is empty")
	}
	if outPath == "" {
		return fmt.Errorf("backup: out path is empty")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("backup: db: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "hami-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	// copy db
	dbCopy := filepath.Join(tmpDir, "panel.db")
	if err := copyFile(dbPath, dbCopy); err != nil {
		return fmt.Errorf("backup: copy db: %w", err)
	}

	var pinData []byte
	if xrayDir != "" {
		pinPath := filepath.Join(xrayDir, "PIN")
		if b, err := os.ReadFile(pinPath); err == nil {
			pinData = b
		}
	}

	manifest := Manifest{
		Version:   "1",
		CreatedAt: time.Now().UTC(),
		HasDB:     true,
		HasPIN:    len(pinData) > 0,
	}
	manRaw, _ := json.MarshalIndent(manifest, "", "  ")

	outFile, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer outFile.Close()

	gw := gzip.NewWriter(outFile)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	if err := writeTarFile(tw, "MANIFEST.json", manRaw, 0o644); err != nil {
		return err
	}
	dbRaw, err := os.ReadFile(dbCopy)
	if err != nil {
		return err
	}
	if err := writeTarFile(tw, "panel.db", dbRaw, 0o600); err != nil {
		return err
	}
	if len(pinData) > 0 {
		if err := writeTarFile(tw, "PIN", pinData, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Restore extracts a backup created by Backup and replaces the database
// and PIN. The database is replaced atomically.
func Restore(inPath, dbPath, xrayDir string) error {
	if inPath == "" || dbPath == "" {
		return fmt.Errorf("restore: path is empty")
	}
	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	tmpDir, err := os.MkdirTemp("", "hami-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	var hasDB bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		// prevent path traversal
		name := filepath.Base(hdr.Name)
		if name == "" || name == "." || name == ".." {
			continue
		}
		dest := filepath.Join(tmpDir, name)
		out, err := os.Create(dest)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return err
		}
		out.Close()
		if name == "panel.db" {
			hasDB = true
		}
	}

	if !hasDB {
		return fmt.Errorf("restore: panel.db not found in archive")
	}

	// ensure target dir exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	// atomic replace: copy to temp file in same dir then rename
	tmpDB := dbPath + ".tmp-restore"
	if err := copyFile(filepath.Join(tmpDir, "panel.db"), tmpDB); err != nil {
		return err
	}
	if err := os.Rename(tmpDB, dbPath); err != nil {
		_ = os.Remove(tmpDB)
		return err
	}

	if xrayDir != "" {
		if _, err := os.Stat(filepath.Join(tmpDir, "PIN")); err == nil {
			if err := os.MkdirAll(xrayDir, 0o755); err != nil {
				return err
			}
			if err := copyFile(filepath.Join(tmpDir, "PIN"), filepath.Join(xrayDir, "PIN")); err != nil {
				return fmt.Errorf("restore: PIN: %w", err)
			}
		}
	}

	return nil
}

func copyFile(src, dst string) error {
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
	return out.Close()
}

func writeTarFile(tw *tar.Writer, name string, data []byte, mode int64) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}
