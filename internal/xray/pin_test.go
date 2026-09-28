package xray

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStageRollsBackWhenCheckFails(t *testing.T) {
	dir := t.TempDir()
	old := []byte("#!/bin/sh\necho old\n")
	if err := os.WriteFile(filepath.Join(dir, "xray"), old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePin(dir, Pin{Version: "26.3.27", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}); err != nil {
		t.Fatal(err)
	}
	bad := []byte("#!/bin/sh\necho bad\nexit 1\n")
	rolled, err := Stage(dir, bad, "26.4.0", func(binPath string) error {
		got, err := os.ReadFile(binPath)
		if err != nil {
			return err
		}
		if string(got) == string(bad) {
			return os.ErrInvalid
		}
		return nil
	})
	if err == nil || !rolled {
		t.Fatalf("rolled=%v err=%v", rolled, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "xray"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("core was not restored: %q", got)
	}
	pin, err := ReadPin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Version != "26.3.27" {
		t.Fatalf("pin changed to %s", pin.Version)
	}
	prev, err := os.ReadFile(filepath.Join(dir, "xray.prev"))
	if err != nil || string(prev) != string(old) {
		t.Fatalf("previous binary missing: %v %q", err, prev)
	}
}

func TestStageKeepsCandidateWhenCheckPasses(t *testing.T) {
	dir := t.TempDir()
	next := []byte("#!/bin/sh\necho 26.3.27\n")
	rolled, err := Stage(dir, next, PinnedVersion, func(string) error { return nil })
	if err != nil || rolled {
		t.Fatalf("rolled=%v err=%v", rolled, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "xray"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(next) {
		t.Fatalf("installed %q", got)
	}
	pin, err := ReadPin(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Version != PinnedVersion || len(pin.SHA256) != 64 {
		t.Fatalf("%+v", pin)
	}
}
