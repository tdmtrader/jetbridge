package native

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/concourse/concourse/atc/runtime"
)

// Compile-time check that Process satisfies runtime.Process.
var _ runtime.Process = (*Process)(nil)

const terminateGracePeriod = 10 * time.Second

// Process implements runtime.Process by wrapping an os/exec.Cmd.
type Process struct {
	id  string
	cmd *exec.Cmd
}

// NewProcess wraps a started exec.Cmd as a runtime.Process.
func NewProcess(cmd *exec.Cmd) *Process {
	return &Process{
		id:  strconv.Itoa(cmd.Process.Pid),
		cmd: cmd,
	}
}

func (p *Process) ID() string {
	return p.id
}

// Wait blocks until the process exits or the context is cancelled. On context
// cancellation, SIGTERM is sent to the process group, followed by SIGKILL
// after a grace period.
func (p *Process) Wait(ctx context.Context) (runtime.ProcessResult, error) {
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- p.cmd.Wait()
	}()

	select {
	case err := <-waitDone:
		if err != nil {
			// A non-zero exit is not an error — extract the exit code.
			if exitErr, ok := err.(*exec.ExitError); ok {
				return runtime.ProcessResult{
					ExitStatus: exitErr.ExitCode(),
				}, nil
			}
			return runtime.ProcessResult{ExitStatus: -1}, fmt.Errorf("wait: %w", err)
		}
		return runtime.ProcessResult{ExitStatus: 0}, nil

	case <-ctx.Done():
		// Send SIGTERM to the process group so child processes are also
		// signalled.
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)

		select {
		case <-waitDone:
			// Process exited after SIGTERM.
		case <-time.After(terminateGracePeriod):
			// Force kill.
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-waitDone
		}

		return runtime.ProcessResult{ExitStatus: -1}, ctx.Err()
	}
}

// SetTTY is not supported for native processes.
func (p *Process) SetTTY(tty runtime.TTYSpec) error {
	return fmt.Errorf("native runner does not support TTY")
}
