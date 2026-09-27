package xray

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeProc struct {
	pid     int
	stopped bool
}

func (p *fakeProc) Pid() int { return p.pid }
func (p *fakeProc) Stop() error {
	p.stopped = true
	return nil
}

type fakeRunner struct {
	starts [][]string
	procs  []*fakeProc
	seen   [][]byte
	dir    string
}

func (f *fakeRunner) Start(ctx context.Context, bin string, args []string) (Proc, error) {
	f.starts = append(f.starts, append([]string{bin}, args...))
	raw, err := os.ReadFile(args[2])
	if err != nil {
		return nil, err
	}
	f.seen = append(f.seen, raw)
	p := &fakeProc{pid: 1000 + len(f.procs)}
	f.procs = append(f.procs, p)
	return p, nil
}

func TestApplyRestartsOntoTheNewConfigOnly(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "xray.json")
	runner := &fakeRunner{}
	s := &Supervisor{Bin: "/usr/local/bin/xray", ConfigPath: cfgPath, Runner: runner}

	a := []byte("{\"inbounds\":[{\"tag\":\"A\",\"privateKey\":\"PRIV_A\"}]}\n")
	b := []byte("{\"inbounds\":[{\"tag\":\"B\",\"privateKey\":\"PRIV_B\"}]}\n")
	if err := s.Apply(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if s.Pid() == 0 {
		t.Fatal("pid not recorded")
	}
	if err := s.Apply(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if !runner.procs[0].stopped {
		t.Fatal("first process was not stopped before the second start")
	}
	if len(runner.starts) != 2 {
		t.Fatalf("starts: %d", len(runner.starts))
	}
	for _, st := range runner.starts {
		if st[1] != "run" || st[2] != "-c" || st[3] != cfgPath {
			t.Fatalf("args: %v", st)
		}
	}
	onDisk, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(b) {
		t.Fatalf("on disk: %s", onDisk)
	}
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode: %v", info.Mode().Perm())
	}
	if string(runner.seen[1]) == string(a) {
		t.Fatal("second start still saw the first config")
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	if s.Pid() != 0 {
		t.Fatal("pid after stop")
	}
}
