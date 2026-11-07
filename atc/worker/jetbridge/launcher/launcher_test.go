package launcher

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// helperEnv marks a re-exec of the test binary that should behave as the
// launcher itself, for the tests that must send it real signals.
const helperEnv = "JB_LAUNCHER_HELPER_PROCESS"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	retryBase = time.Millisecond
	retryCap = 5 * time.Millisecond
	os.Exit(m.Run())
}

// runLauncher runs the in-process launcher with buffered stdout/stderr and a
// termination message in a temp dir, returning the code, outputs and message path.
func runLauncher(t *testing.T, args ...string) (code int, stdout, stderr string, termPath string) {
	t.Helper()
	termPath = filepath.Join(t.TempDir(), "term")
	var out, errb bytes.Buffer
	full := append([]string{"--termination-message", termPath}, args...)
	code = Run(full, nil, &out, &errb)
	return code, out.String(), errb.String(), termPath
}

func readTermination(t *testing.T, path string) terminationMessage {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading termination message: %v", err)
	}
	if !bytes.HasSuffix(raw, []byte("\n")) || bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatalf("termination message is not one line: %q", raw)
	}
	var msg terminationMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("termination message %q: %v", raw, err)
	}
	return msg
}

func TestExitCodes(t *testing.T) {
	notExec := filepath.Join(t.TempDir(), "not-executable")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\nexit 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		args    []string
		want    int
		errText string
	}{
		{"zero", []string{"--", "true"}, 0, ""},
		{"propagates child exit", []string{"--", "sh", "-c", "exit 7"}, 7, ""},
		{"died by signal", []string{"--", "sh", "-c", "kill -9 $$"}, 128 + 9, ""},
		{"no command", []string{}, 125, "no command given"},
		{"only separator", []string{"--"}, 125, "usage:"},
		{"not found", []string{"--", "jb-launcher-no-such-command-xyz"}, 127, "starting"},
		{"not found by path", []string{"--", "/nonexistent/dir/cmd"}, 127, "starting"},
		{"not executable", []string{"--", notExec}, 126, "starting"},
		{"missing token file", []string{"--envelope-url", "http://127.0.0.1:1/", "--token-file", "/nonexistent/token", "--", "true"}, 125, "reading token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr, _ := runLauncher(t, tc.args...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tc.want, stderr)
			}
			if tc.errText != "" && !strings.Contains(stderr, tc.errText) {
				t.Fatalf("stderr %q does not contain %q", stderr, tc.errText)
			}
			if tc.errText != "" && tc.name != "only separator" && !strings.Contains(stderr, "jb-launcher: ") {
				t.Fatalf("stderr %q lacks the jb-launcher: prefix", stderr)
			}
		})
	}
}

func TestTerminationMessage(t *testing.T) {
	before := time.Now().Add(-time.Second)
	code, _, _, termPath := runLauncher(t, "--", "sh", "-c", "exit 3")
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	msg := readTermination(t, termPath)
	if msg.Exit != 3 || msg.StdoutBytes != 0 {
		t.Fatalf("message = %+v", msg)
	}
	if msg.StartedAt.Before(before) || msg.FinishedAt.Before(msg.StartedAt) {
		t.Fatalf("timestamps out of order: %+v", msg)
	}

	t.Run("unwritable path does not fail the step", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := Run([]string{"--termination-message", "/nonexistent/dir/term", "--", "sh", "-c", "exit 4"}, nil, &out, &errb)
		if code != 4 {
			t.Fatalf("exit = %d, want 4 (stderr %q)", code, errb.String())
		}
	})
}

func TestStdoutFile(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "stdout")
	if err := os.WriteFile(outPath, []byte("stale content that must be truncated"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr, termPath := runLauncher(t, "--stdout-file", outPath, "--", "sh", "-c", "echo hello; echo oops >&2")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n" {
		t.Fatalf("stdout file = %q", got)
	}
	if stdout != "" {
		t.Fatalf("launcher stdout should be empty, got %q", stdout)
	}
	if stderr != "oops\n" {
		t.Fatalf("stderr should still inherit, got %q", stderr)
	}
	if msg := readTermination(t, termPath); msg.StdoutBytes != 6 {
		t.Fatalf("stdout_bytes = %d, want 6", msg.StdoutBytes)
	}
	// outPath pre-existed with 0600 and O_CREATE does not chmod, so the
	// 0644 claim is checked on a freshly created file.
	fresh := filepath.Join(t.TempDir(), "fresh")
	if code, _, stderr, _ := runLauncher(t, "--stdout-file", fresh, "--", "true"); code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if st, err := os.Stat(fresh); err != nil || st.Mode().Perm()&0o644 != 0o644 {
		t.Fatalf("fresh stdout file mode = %v, err %v", st.Mode().Perm(), err)
	}
}

// envelopeServer serves the step envelope, checking the request shape, and
// answering with the given status sequence before the final 200.
func envelopeServer(t *testing.T, token, stdin string, failFirst ...int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		body, _ := readAll(r)
		if body != "{}" {
			t.Errorf("body = %q", body)
		}
		if n <= len(failFirst) {
			http.Error(w, "flaky", failFirst[n-1])
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(envelope{Stdin: base64.StdEncoding.EncodeToString([]byte(stdin))})
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, &calls
}

func readAll(r *http.Request) (string, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.String(), err
}

func writeToken(t *testing.T, token string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStdinFromEnvelope(t *testing.T) {
	srv, calls := envelopeServer(t, "secret-token", "from the envelope\n")
	code, stdout, stderr, _ := runLauncher(t,
		"--envelope-url", srv.URL+"/envelope",
		"--token-file", writeToken(t, "secret-token"),
		"--", "cat")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if stdout != "from the envelope\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestEmptyEnvelopeStdinIsDevNull(t *testing.T) {
	srv, _ := envelopeServer(t, "tok", "")
	code, stdout, stderr, _ := runLauncher(t,
		"--envelope-url", srv.URL,
		"--token-file", writeToken(t, "tok"),
		"--", "cat")
	if code != 0 || stdout != "" {
		t.Fatalf("exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}
}

func TestEnvelope5xxThen200Retries(t *testing.T) {
	srv, calls := envelopeServer(t, "tok", "eventually\n", 503, 500)
	code, stdout, stderr, _ := runLauncher(t,
		"--envelope-url", srv.URL,
		"--token-file", writeToken(t, "tok"),
		"--", "cat")
	if code != 0 {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if stdout != "eventually\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestEnvelope4xxIsFatal(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "build is gone", http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	code, stdout, stderr, _ := runLauncher(t,
		"--envelope-url", srv.URL,
		"--token-file", writeToken(t, "tok"),
		"--", "sh", "-c", "echo ran")
	if code != 125 {
		t.Fatalf("exit = %d, want 125 (stderr %q)", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("the command must not run after a fatal fetch, stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "403") || !strings.Contains(stderr, "build is gone") {
		t.Fatalf("stderr = %q", stderr)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestEnvelopeConnectionErrorsRetryUntilTimeout(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // nothing listens any more
	start := time.Now()
	code, _, stderr, _ := runLauncher(t,
		"--envelope-url", url,
		"--envelope-timeout", "60ms",
		"--token-file", writeToken(t, "tok"),
		"--", "true")
	if code != 125 {
		t.Fatalf("exit = %d, want 125 (stderr %q)", code, stderr)
	}
	if !strings.Contains(stderr, "gave up after 60ms") {
		t.Fatalf("stderr = %q", stderr)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
}

func TestEnvelopeHTTPSWithCAFile(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(envelope{Stdin: base64.StdEncoding.EncodeToString([]byte("tls\n"))})
	}))
	t.Cleanup(srv.Close)
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	block := &pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}
	if err := os.WriteFile(caPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr, _ := runLauncher(t,
		"--envelope-url", srv.URL,
		"--ca-file", caPath,
		"--token-file", writeToken(t, "tok"),
		"--", "cat")
	if code != 0 || stdout != "tls\n" {
		t.Fatalf("exit = %d stdout = %q stderr = %q", code, stdout, stderr)
	}

	t.Run("untrusted without ca-file", func(t *testing.T) {
		code, _, stderr, _ := runLauncher(t,
			"--envelope-url", srv.URL,
			"--envelope-timeout", "30ms",
			"--token-file", writeToken(t, "tok"),
			"--", "cat")
		if code != 125 || !strings.Contains(stderr, "fetching step envelope") {
			t.Fatalf("exit = %d stderr = %q", code, stderr)
		}
	})
}

// startHelper re-executes the test binary as the launcher with the given
// args, stdout/stderr to a file so a stray grandchild cannot hold a pipe open.
func startHelper(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	logFile, err := os.Create(filepath.Join(t.TempDir(), "helper.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}
	return cmd
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

func waitExit(t *testing.T, cmd *exec.Cmd, within time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return statusExitCode(exitErr.Sys().(syscall.WaitStatus))
		}
		t.Fatalf("waiting for the launcher: %v", err)
		return -1
	case <-time.After(within):
		_ = cmd.Process.Kill()
		t.Fatalf("launcher did not exit within %s", within)
		return -1
	}
}

func TestSIGTERMIsForwardedToTheChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM on windows")
	}
	ready := filepath.Join(t.TempDir(), "ready")
	termPath := filepath.Join(t.TempDir(), "term")
	cmd := startHelper(t,
		"--termination-message", termPath,
		"--kill-grace", "10s",
		"--", "sh", "-c", `trap "exit 3" TERM; touch "$0"; sleep 30 & wait`, ready)
	waitForFile(t, ready)

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, cmd, 5*time.Second); code != 3 {
		t.Fatalf("launcher exit = %d, want 3 (the child's trap)", code)
	}
	if msg := readTermination(t, termPath); msg.Exit != 3 {
		t.Fatalf("termination message = %+v", msg)
	}
}

func TestKillGraceEscalatesToSIGKILL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no signals on windows")
	}
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := startHelper(t,
		"--termination-message", filepath.Join(t.TempDir(), "term"),
		"--kill-grace", "200ms",
		"--", "sh", "-c", `trap "" TERM; touch "$0"; sleep 30`, ready)
	waitForFile(t, ready)

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, cmd, 5*time.Second); code != 128+9 {
		t.Fatalf("launcher exit = %d, want 137", code)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatalf("exited after %s, before the grace period", time.Since(start))
	}
}

// An orphan the command leaves behind is reaped by the launcher, which is
// what a pod's PID 1 owes everything below it: a zombie would answer
// kill(pid, 0) as alive for as long as the pod lived.
func TestOrphansAreReaped(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the launcher is a child subreaper on Linux only")
	}
	// The inner shell's parent is the launcher. After the orphan has exited
	// and had time to be reaped, no zombie child of the launcher may remain.
	script := `(sleep 0.2 &); sleep 0.8; z=0; for s in /proc/[0-9]*/status; do ` +
		`if grep -q "^PPid:[[:space:]]*$PPID\$" "$s" 2>/dev/null && grep -q "^State:[[:space:]]*Z" "$s" 2>/dev/null; then z=1; fi; done; exit $z`
	code := Run([]string{"--", "sh", "-c", script}, nil, io.Discard, os.Stderr)
	if code != 0 {
		t.Fatalf("exit %d: a zombie child of the launcher remained", code)
	}
}
