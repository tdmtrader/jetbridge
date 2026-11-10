package launcher

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	exitUsage      = 125 // launcher-internal failure, also bad usage
	exitNotExec    = 126
	exitNotFound   = 127
	exitSignalBase = 128

	maxErrorBody = 1024
)

// Retry schedule for the envelope fetch. Variables so tests can shrink them.
var (
	retryBase = time.Second
	retryCap  = 10 * time.Second
)

// httpAttemptTimeout bounds one request to the envelope URL.
const httpAttemptTimeout = 30 * time.Second

// envelope is the body the envelope URL answers with.
type envelope struct {
	Stdin string `json:"stdin"`
}

// terminationMessage is the one JSON line left for web after the child exits.
type terminationMessage struct {
	Exit        int       `json:"exit"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
	StdoutBytes int64     `json:"stdout_bytes"`
}

// run is main without the process: it parses args, fetches the envelope,
// runs the command and returns the launcher's exit code. stdout and stderr
// are what the child inherits (an *os.File is handed over as-is, anything
// else is piped). The launcher's own stdin is never given to the child: the
// child reads the envelope's stdin, or /dev/null when there is none.
func Run(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "jb-launcher: "+format+"\n", a...)
		return exitUsage
	}

	fs := flag.NewFlagSet("jb-launcher", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: jb-launcher [flags] -- <command> [args...]")
		fs.PrintDefaults()
	}
	envelopeURL := fs.String("envelope-url", "", "URL to POST to for the step envelope (optional)")
	tokenFile := fs.String("token-file", "/var/run/jetbridge/token", "file holding the bearer token sent to --envelope-url")
	caFile := fs.String("ca-file", "", "PEM CA bundle to trust for an https --envelope-url (default: system roots)")
	envelopeTimeout := fs.Duration("envelope-timeout", 10*time.Minute, "how long to keep retrying the envelope fetch; a web restarting mid-step registers the envelope again when it resumes the step")
	stdoutFile := fs.String("stdout-file", "", "write the command's stdout to this file instead of inheriting")
	termPath := fs.String("termination-message", "/dev/termination-log", "where to write the JSON exit record")
	killGrace := fs.Duration("kill-grace", 25*time.Second, "time between forwarding SIGTERM/SIGINT and SIGKILLing the process group")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitUsage
		}
		return fail("%v", err)
	}
	command := fs.Args()
	if len(command) == 0 {
		fs.Usage()
		return fail("no command given")
	}

	var childStdin io.Reader
	if *envelopeURL != "" {
		token, err := os.ReadFile(*tokenFile)
		if err != nil {
			return fail("reading token: %v", err)
		}
		stdinBytes, err := fetchEnvelope(*envelopeURL, strings.TrimSpace(string(token)), *caFile, *envelopeTimeout)
		if err != nil {
			return fail("fetching step envelope: %v", err)
		}
		if len(stdinBytes) > 0 {
			// A file, not a pipe: exec.Cmd copies a non-file stdin through
			// a goroutine that only Wait collects, and the launcher reaps
			// its children itself (see below) rather than calling Wait.
			f, err := stdinFile(stdinBytes)
			if err != nil {
				return fail("staging stdin: %v", err)
			}
			defer f.Close()
			childStdin = f
		}
	}

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = childStdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var outFile *os.File
	if *stdoutFile != "" {
		f, err := os.OpenFile(*stdoutFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fail("opening stdout file: %v", err)
		}
		defer f.Close()
		outFile = f
		cmd.Stdout = f
	}

	// Install the handler before Start so a signal in the window between
	// the two is queued rather than killing the launcher by default action.
	// SIGHUP is left at its default: an ignored disposition is inherited
	// across exec, and a command that inherits SIGHUP ignored behaves
	// differently from one run from a shell (a test that traps SIGHUP, say).
	// Nothing hangs up on a step pod's main container anyway.
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigs)

	// The launcher is the pod's PID 1 (and, where the platform allows, the
	// subreaper of everything below it): whatever the command leaves behind
	// is reparented to it and must be reaped, or it stays a zombie for as
	// long as the pod lives -- and a zombie answers kill(pid, 0) as alive.
	becomeSubreaper()

	startedAt := time.Now()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "jb-launcher: starting %q: %v\n", command[0], err)
		// The command did not run, and that is as much its outcome as an
		// exit is: the record says so, with the code returned.
		code := startExitCode(err)
		writeTerminationMessage(*termPath, terminationMessage{Exit: code, StartedAt: startedAt, FinishedAt: time.Now()})
		return code
	}
	pgid := cmd.Process.Pid // Setpgid makes the child the leader of its own group

	done := make(chan struct{})
	go forwardSignals(sigs, done, pgid, *killGrace)

	status := reapUntil(cmd.Process.Pid)
	close(done)
	finishedAt := time.Now()
	code := statusExitCode(status)

	var stdoutBytes int64
	if outFile != nil {
		if st, err := outFile.Stat(); err == nil {
			stdoutBytes = st.Size()
		}
	}
	writeTerminationMessage(*termPath, terminationMessage{
		Exit:        code,
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		StdoutBytes: stdoutBytes,
	})
	return code
}

// forwardSignals relays SIGTERM/SIGINT to the child's process group and
// SIGKILLs the group if it is still around after grace. It returns when done
// is closed (the child has exited).
func forwardSignals(sigs <-chan os.Signal, done <-chan struct{}, pgid int, grace time.Duration) {
	var escalate <-chan time.Time
	for {
		select {
		case <-done:
			return
		case sig := <-sigs:
			if s, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(-pgid, s)
			}
			if escalate == nil {
				escalate = time.After(grace)
			}
		case <-escalate:
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
			escalate = nil
		}
	}
}

// startExitCode maps an exec.Cmd.Start error onto the shell convention.
func startExitCode(err error) int {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, fs.ErrNotExist):
		return exitNotFound
	case errors.Is(err, fs.ErrPermission), errors.Is(err, syscall.ENOEXEC), errors.Is(err, syscall.EACCES):
		return exitNotExec
	}
	return exitUsage
}

// reapUntil waits for every child of the launcher, reaping orphans the
// command leaves behind, until the command itself (child) has exited, and
// returns its wait status. A final sweep takes what exited with it.
func reapUntil(child int) syscall.WaitStatus {
	var status syscall.WaitStatus
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			// ECHILD with the command unreaped cannot happen; treat it as
			// a signalled death so the step fails rather than hangs.
			status = syscall.WaitStatus(syscall.SIGKILL)
			break
		}
		if pid == child {
			status = ws
			break
		}
	}
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err != nil || pid <= 0 {
			break
		}
	}
	return status
}

// statusExitCode turns the command's wait status into the launcher's exit
// code: the command's own, or 128 plus the signal that killed it.
func statusExitCode(ws syscall.WaitStatus) int {
	if ws.Signaled() {
		return exitSignalBase + int(ws.Signal())
	}
	if ws.Exited() {
		return ws.ExitStatus()
	}
	return exitUsage
}

// stdinFile stages the envelope's stdin in a temporary file the command
// reads directly.
func stdinFile(content []byte) (*os.File, error) {
	f, err := os.CreateTemp("", "jb-launcher-stdin-")
	if err != nil {
		return nil, err
	}
	_ = os.Remove(f.Name())
	if _, err := f.Write(content); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// writeTerminationMessage is best-effort: a step must not fail because the
// container runtime's termination log is unwritable.
func writeTerminationMessage(path string, msg terminationMessage) {
	if path == "" {
		return
	}
	line, err := json.Marshal(msg)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// fetchEnvelope POSTs to url until it answers 200, retrying connection
// errors and 5xx with exponential backoff for up to timeout. Any other
// status is fatal at once. It returns the decoded stdin bytes.
func fetchEnvelope(url, token, caFile string, timeout time.Duration) ([]byte, error) {
	client, err := newHTTPClient(caFile)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	backoff := retryBase
	var lastErr error
	for attempt := 1; ; attempt++ {
		stdin, retry, err := fetchEnvelopeOnce(ctx, client, url, token)
		if err == nil {
			return stdin, nil
		}
		if !retry {
			return nil, err
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("gave up after %s and %d attempt(s): %w", timeout, attempt, lastErr)
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > retryCap {
			backoff = retryCap
		}
	}
}

// fetchEnvelopeOnce makes one request. retry reports whether the failure is
// one the schedule should retry (connection error or 5xx).
func fetchEnvelopeOnce(ctx context.Context, client *http.Client, url, token string) (stdin []byte, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("{}"))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var env envelope
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&env); err != nil {
			return nil, false, fmt.Errorf("decoding envelope: %w", err)
		}
		if env.Stdin == "" {
			return nil, false, nil
		}
		raw, err := base64.StdEncoding.DecodeString(env.Stdin)
		if err != nil {
			return nil, false, fmt.Errorf("decoding envelope stdin: %w", err)
		}
		return raw, false, nil
	case resp.StatusCode >= 500:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		return nil, true, fmt.Errorf("%s", resp.Status)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return nil, false, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

func newHTTPClient(caFile string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", caFile)
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: transport, Timeout: httpAttemptTimeout}, nil
}
