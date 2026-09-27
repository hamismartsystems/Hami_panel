package xray

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// Proc is one running core. Stop must be safe to call once.
type Proc interface {
	Pid() int
	Stop() error
}

// Runner starts the core. Tests pass a fake; production uses ExecRunner.
type Runner interface {
	Start(ctx context.Context, bin string, args []string) (Proc, error)
}

// Supervisor writes a config and restarts the core so the running process
// is serving that file and no older one. Reload is a restart on purpose:
// a silent hot-reload can keep yesterday's Reality key.
type Supervisor struct {
	Bin        string
	ConfigPath string
	Runner     Runner

	proc Proc
}

// Apply writes cfg (mode 0600) and starts the core with `run -c` that path.
// If a process is already running, it is stopped first.
func (s *Supervisor) Apply(ctx context.Context, cfg []byte) error {
	if s.Runner == nil {
		return fmt.Errorf("xray runner is nil")
	}
	if s.Bin == "" {
		return fmt.Errorf("xray binary path is empty")
	}
	if s.ConfigPath == "" {
		return fmt.Errorf("xray config path is empty")
	}
	if len(cfg) == 0 {
		return fmt.Errorf("xray config is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.ConfigPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(s.ConfigPath, cfg, 0o600); err != nil {
		return err
	}
	if s.proc != nil {
		if err := s.proc.Stop(); err != nil {
			return fmt.Errorf("stop previous xray: %w", err)
		}
		s.proc = nil
	}
	p, err := s.Runner.Start(ctx, s.Bin, []string{"run", "-c", s.ConfigPath})
	if err != nil {
		return err
	}
	s.proc = p
	return nil
}

// Stop stops the core if one is running.
func (s *Supervisor) Stop() error {
	if s.proc == nil {
		return nil
	}
	err := s.proc.Stop()
	s.proc = nil
	return err
}

// Pid returns the running core's pid, or 0.
func (s *Supervisor) Pid() int {
	if s.proc == nil {
		return 0
	}
	return s.proc.Pid()
}
