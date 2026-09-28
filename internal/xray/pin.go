package xray

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// PinnedVersion is the core release the live canary was run against.
// An upgrade may move past it only after its own check passes.
const PinnedVersion = "26.3.27"

// Pin is the core currently installed by this panel.
type Pin struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

// ReadPin reads dir/PIN.
func ReadPin(dir string) (Pin, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "PIN"))
	if err != nil {
		return Pin{}, err
	}
	var pin Pin
	if err := json.Unmarshal(raw, &pin); err != nil {
		return Pin{}, fmt.Errorf("pin: %w", err)
	}
	if pin.Version == "" || len(pin.SHA256) != 64 {
		return Pin{}, fmt.Errorf("pin: incomplete")
	}
	return pin, nil
}

// Stage installs candidate as dir/xray. The previous binary is kept as
// xray.prev. If check fails, that previous binary is put back and the pin
// is not changed. A bad core is never left in place.
func Stage(dir string, candidate []byte, version string, check func(binPath string) error) (rolledBack bool, err error) {
	if version == "" {
		return false, fmt.Errorf("pin: version is empty")
	}
	if len(candidate) == 0 {
		return false, fmt.Errorf("pin: candidate is empty")
	}
	if check == nil {
		return false, fmt.Errorf("pin: check is nil")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	bin := filepath.Join(dir, "xray")
	prev := filepath.Join(dir, "xray.prev")
	next := filepath.Join(dir, "xray.next")
	sum := sha256.Sum256(candidate)

	current, readErr := os.ReadFile(bin)
	hadCurrent := readErr == nil && len(current) > 0
	if hadCurrent {
		if err := os.WriteFile(prev, current, 0o755); err != nil {
			return false, fmt.Errorf("pin: save previous: %w", err)
		}
	}
	oldPin, pinErr := ReadPin(dir)
	hadPin := pinErr == nil

	if err := os.WriteFile(next, candidate, 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(next, bin); err != nil {
		_ = os.Remove(next)
		return false, err
	}
	if err := check(bin); err != nil {
		if hadCurrent {
			if rerr := os.WriteFile(bin, current, 0o755); rerr != nil {
				return true, fmt.Errorf("pin: check failed (%v) and rollback failed: %w", err, rerr)
			}
		} else {
			_ = os.Remove(bin)
		}
		if hadPin {
			_ = writePin(dir, oldPin)
		} else {
			_ = os.Remove(filepath.Join(dir, "PIN"))
		}
		return true, fmt.Errorf("pin: check failed, previous core restored: %w", err)
	}
	if err := writePin(dir, Pin{Version: version, SHA256: hex.EncodeToString(sum[:])}); err != nil {
		return false, err
	}
	return false, nil
}

func writePin(dir string, pin Pin) error {
	raw, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(dir, "PIN"), raw, 0o644)
}
