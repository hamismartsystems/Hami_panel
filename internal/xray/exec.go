package xray

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// ExecRunner starts a real xray process. The sandbox tests do not use it;
// a deployment does.
type ExecRunner struct{}

type execProc struct {
	cmd *exec.Cmd
}

// Start runs bin with args. The process is in its own group so Stop can
// signal the whole group.
func (ExecRunner) Start(ctx context.Context, bin string, args []string) (Proc, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd: cmd}, nil
}

func (p *execProc) Pid() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *execProc) Stop() error {
	if p.cmd.Process == nil {
		return nil
	}
	// Negative pid signals the process group created at Start.
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	return p.cmd.Wait()
}
