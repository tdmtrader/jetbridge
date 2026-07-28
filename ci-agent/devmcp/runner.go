package devmcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Status values mirror the contract taxonomy (§3.1): ok = passed,
// failed = ran and found problems, error = tooling broke.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
	StatusError  = "error"
)

const tailLines = 200

// ToolResult is the §3.1 shared result payload for build/run_tests/lint
// (wire shape mirrored from the main module's agent/devmcp, which this
// standalone module cannot import; the contract kit enforces conformance).
type ToolResult struct {
	Status          string    `json:"status"`
	Summary         string    `json:"summary"`
	DurationSeconds float64   `json:"duration_seconds"`
	OutputTail      string    `json:"output_tail,omitempty"`
	LogPath         string    `json:"log_path,omitempty"`
	Failures        []Failure `json:"failures,omitempty"`
}

// Failure is one structured failure. The v1 reference implementation never
// populates these (the field is optional in the contract).
type Failure struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// lineTail is an io.Writer that keeps the last tailLines completed lines,
// reports each completed line to progress, and mirrors all bytes to an
// underlying writer (the log file). It is safe for the concurrent
// stdout/stderr writes exec.Cmd performs.
type lineTail struct {
	mu       sync.Mutex
	mirror   io.Writer
	lines    []string
	partial  strings.Builder
	progress ProgressFunc
	writeErr error
}

func (t *lineTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.mirror != nil {
		written, err := t.mirror.Write(p)
		if written < 0 || written > len(p) {
			err = fmt.Errorf("invalid mirror write count %d", written)
			written = 0
		}
		if written > 0 {
			t.consume(p[:written])
		}
		if err == nil && written != len(p) {
			err = io.ErrShortWrite
		}
		if err != nil {
			if t.writeErr == nil {
				t.writeErr = err
			}
			return written, err
		}
		return len(p), nil
	}
	t.consume(p)
	return len(p), nil
}

func (t *lineTail) consume(p []byte) {
	for _, b := range p {
		if b == '\n' {
			line := t.partial.String()
			t.partial.Reset()
			t.lines = append(t.lines, line)
			if len(t.lines) > tailLines {
				t.lines = t.lines[1:]
			}
			if t.progress != nil && strings.TrimSpace(line) != "" {
				t.progress(line)
			}
		} else {
			t.partial.WriteByte(b)
		}
	}
}

func (t *lineTail) Tail() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// runCommand executes one CommandSpec under workdir and classifies its exit
// per the contract's exit-code convention: 0 → ok; failed_exit_codes
// (default [1]) → failed; anything else — other exit codes, spawn failure,
// context cancellation — → error.
func runCommand(ctx context.Context, workdir, label string, spec CommandSpec, extraArgs []string, progress ProgressFunc) ToolResult {
	return runCommandWithLogFactory(ctx, workdir, label, spec, extraArgs, progress, func(path string) (io.WriteCloser, error) {
		return os.Create(path)
	})
}

func runCommandWithLogFactory(
	ctx context.Context,
	workdir, label string,
	spec CommandSpec,
	extraArgs []string,
	progress ProgressFunc,
	createLog func(string) (io.WriteCloser, error),
) ToolResult {
	start := time.Now()

	relLog := filepath.Join(".dev-mcp", "logs", fmt.Sprintf("%s-%d.log", label, start.UnixNano()))
	absLog := filepath.Join(workdir, relLog)
	if err := os.MkdirAll(filepath.Dir(absLog), 0o755); err != nil {
		return errorResult(label, start, "", fmt.Sprintf("create log dir: %s", err))
	}
	logFile, err := createLog(absLog)
	if err != nil {
		return errorResult(label, start, "", fmt.Sprintf("create log file: %s", err))
	}

	tail := &lineTail{mirror: logFile, progress: progress}

	args := append(append([]string{}, spec.Cmd[1:]...), extraArgs...)
	cmd := exec.CommandContext(ctx, spec.Cmd[0], args...)
	cmd.Dir = filepath.Join(workdir, spec.Dir) // spec.Dir == "" keeps workdir
	cmd.Stdout = tail
	cmd.Stderr = tail

	runErr := cmd.Run()
	closeErr := logFile.Close()
	duration := time.Since(start).Seconds()

	status := StatusOK
	detail := "exit 0"
	if runErr != nil {
		status = StatusError
		detail = runErr.Error()
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && ctx.Err() == nil {
			code := exitErr.ExitCode()
			detail = fmt.Sprintf("exit %d", code)
			for _, failed := range spec.failedCodes() {
				if code == failed {
					status = StatusFailed
					break
				}
			}
		}
	}
	if tail.writeErr != nil || closeErr != nil {
		status = StatusError
		var logErrors []error
		if tail.writeErr != nil {
			logErrors = append(logErrors, fmt.Errorf("write log file: %w", tail.writeErr))
		}
		if closeErr != nil {
			logErrors = append(logErrors, fmt.Errorf("close log file: %w", closeErr))
		}
		detail = errors.Join(logErrors...).Error()
	}

	return ToolResult{
		Status:          status,
		Summary:         fmt.Sprintf("%s: %s (%s) in %.1fs", label, status, detail, duration),
		DurationSeconds: duration,
		OutputTail:      tail.Tail(),
		LogPath:         relLog,
	}
}

func (s CommandSpec) failedCodes() []int {
	if len(s.FailedExitCodes) == 0 {
		return []int{1}
	}
	return s.FailedExitCodes
}

func errorResult(label string, start time.Time, logPath, msg string) ToolResult {
	return ToolResult{
		Status:          StatusError,
		Summary:         fmt.Sprintf("%s: error (%s)", label, msg),
		DurationSeconds: time.Since(start).Seconds(),
		LogPath:         logPath,
	}
}
