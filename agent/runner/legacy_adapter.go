package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"

	"github.com/concourse/concourse/agent/provider"
)

// legacyClaudeAdapter retains the existing stream-json CLI launch exactly,
// while making its lack of live safe-boundary evidence explicit.
type legacyClaudeAdapter struct {
	path      string
	args      []string
	env       []string
	stderr    io.Writer
	waitDelay time.Duration
}

func (legacyClaudeAdapter) Identity() provider.Identity {
	return provider.Identity{Name: "claude-cli", Version: "legacy-stream-json"}
}

func (legacyClaudeAdapter) Capabilities() provider.Capabilities {
	// Generic Claude stream-json records tool transcript events, but does not
	// prove the CLI is paused after a completed batch before another model call.
	return provider.Capabilities{}
}

func (adapter legacyClaudeAdapter) Start(ctx context.Context, request provider.StartRequest, control provider.BoundaryControl) (provider.RunningSession, error) {
	if control != nil {
		return nil, errors.New("legacy Claude adapter cannot accept safe-boundary control")
	}
	var stream bytes.Buffer
	cmd := exec.CommandContext(ctx, adapter.path, adapter.args...)
	// Keep Claude outside the supervisor foreground group. Cancellation still
	// kills its whole detached group, including leaked tool subprocesses.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.Dir = request.WorkDir
	cmd.Env = adapter.env
	// Stdin stays nil. The supervised process must not use the historical
	// stdin frame protocol, or web-restart attach would no longer be safe.
	cmd.Stdout = io.MultiWriter(&stream, requestOutput(request))
	cmd.Stderr = adapter.stderr
	cmd.WaitDelay = adapter.waitDelay
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &legacyClaudeSession{cmd: cmd, stream: &stream}, nil
}

// requestOutput deliberately uses the runner-owned writer supplied in the
// adapter request. It never uses stdin; supervisor reattach remains enabled.
func requestOutput(request provider.StartRequest) io.Writer {
	if request.Stdout != nil {
		return request.Stdout
	}
	return io.Discard
}

// legacyClaudeSession owns exactly the old cmd.Wait semantics.
type legacyClaudeSession struct {
	cmd    *exec.Cmd
	stream *bytes.Buffer
}

func (session *legacyClaudeSession) Wait(context.Context) (provider.Result, error) {
	err := session.cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	return provider.Result{Stream: append([]byte(nil), session.stream.Bytes()...)}, err
}
