package session

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var ErrCredentialHandoffTimeout = errors.New("credential handoff timed out")

// ListenCredentialHandoff accepts one credential stream for an already-started
// worker. Its socket lives in that worker's private tmpfs. The control plane must
// authorize the owner and exact Run/Pod before invoking the helper; this local
// transport does not authenticate a remote principal.
func ListenCredentialHandoff(ctx context.Context, runtimeDir, path string, runID int, timeout time.Duration) (io.ReadCloser, error) {
	if runID <= 0 || timeout <= 0 {
		return nil, errors.New("positive Run ID and handoff timeout are required")
	}
	if err := RequireMemoryRuntime(runtimeDir); err != nil {
		return nil, err
	}
	parent, err := filepath.EvalSymlinks(runtimeDir)
	if err != nil {
		return nil, errors.New("credential runtime is unavailable")
	}
	parent, err = filepath.Abs(parent)
	if err != nil || !filepath.IsAbs(path) || filepath.Dir(path) != parent {
		return nil, errors.New("credential socket must be directly inside the private runtime")
	}
	st, err := os.Stat(parent)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0700 {
		return nil, errors.New("socket handoff requires a mode-0700 runtime directory")
	}
	// Never unlink a previous worker's endpoint to reseed or steal its session.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, errors.New("credential socket is unavailable")
	}
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return nil, errors.New("cannot protect credential socket")
	}
	handoffCtx, cancel := context.WithTimeout(ctx, timeout)
	s := &credentialSocket{listener: listener, ctx: handoffCtx, cancel: cancel, runID: runID}
	context.AfterFunc(handoffCtx, func() { _ = s.Close() })
	return s, nil
}

type credentialSocket struct {
	listener *net.UnixListener
	ctx      context.Context
	cancel   context.CancelFunc
	runID    int
	once     sync.Once
	mu       sync.Mutex
	conn     *net.UnixConn
	reader   *bufio.Reader
	err      error
	closed   bool
}

func (s *credentialSocket) Read(p []byte) (int, error) {
	s.once.Do(func() {
		conn, err := s.listener.AcceptUnix()
		if err != nil {
			s.err = errors.New("credential handoff unavailable")
			return
		}
		// Exactly one connection: even a rejected or interrupted first attempt
		// consumes this worker's handoff. A fresh submission gets a new worker.
		_ = s.listener.Close()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			s.err = errors.New("credential handoff closed")
			return
		}
		s.conn = conn
		s.mu.Unlock()
		deadline, _ := s.ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		s.reader = bufio.NewReaderSize(conn, 64)
		header, err := s.reader.ReadSlice('\n')
		if err != nil || string(header) != credentialHeader(s.runID) {
			s.err = errors.New("credential handoff identity mismatch")
		}
	})
	if s.err != nil {
		return 0, s.readError(s.err)
	}
	n, err := s.reader.Read(p)
	return n, s.readError(err)
}

func (s *credentialSocket) readError(err error) error {
	if errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
		return ErrCredentialHandoffTimeout
	}
	if timed, ok := err.(net.Error); ok && timed.Timeout() {
		return ErrCredentialHandoffTimeout
	}
	return err
}

// Acknowledge is called only after the worker has staged the credentials and
// checked its provider executable. Closing the helper cannot cancel the worker.
func (s *credentialSocket) Acknowledge() error {
	s.mu.Lock()
	conn, closed := s.conn, s.closed
	s.mu.Unlock()
	if closed || conn == nil {
		return errors.New("credential handoff closed before readiness")
	}
	_, err := io.WriteString(conn, credentialReady(s.runID))
	_ = s.Close()
	if err != nil {
		return errors.New("credential readiness acknowledgement failed")
	}
	return nil
}

func (s *credentialSocket) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	conn := s.conn
	s.mu.Unlock()
	// Cancellation itself is idempotent; no recursive Close is waited on.
	s.cancel()
	_ = s.listener.Close()
	if conn != nil {
		_ = conn.Close()
	}
	return nil
}

func credentialHeader(runID int) string { return fmt.Sprintf("jb-review-auth/v1 %d\n", runID) }
func credentialReady(runID int) string  { return fmt.Sprintf("ready %d\n", runID) }

// SendCredentialHandoff is the in-container exec helper. Auth travels only over
// stdin and the private socket. No credential path or contents enter arguments,
// environment, output, a Kubernetes object or a durable intermediary.
func SendCredentialHandoff(ctx context.Context, path string, runID int, auth io.ReadCloser) error {
	if auth == nil {
		return errors.New("credential input is required")
	}
	defer auth.Close()
	if runID <= 0 || !filepath.IsAbs(path) {
		return errors.New("positive Run ID and absolute credential socket are required")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("credential handoff requires a deadline")
	}
	stop := context.AfterFunc(ctx, func() { _ = auth.Close() })
	defer stop()
	data, err := io.ReadAll(io.LimitReader(auth, (64<<10)+1))
	defer clear(data)
	if err != nil || len(data) > 64<<10 || (Codex{}).CheckAuth(data) != nil {
		return errors.New("valid Codex subscription auth.json required")
	}
	// A signed execution start can precede the worker opening its socket.
	// Retry only an absent/not-yet-listening endpoint, before any connection
	// accepted bytes. An established connection is never retried or reseeded.
	var conn net.Conn
	for {
		conn, err = (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.ENOENT) && !errors.Is(err, syscall.ECONNREFUSED) {
			return errors.New("credential socket is unavailable")
		}
		select {
		case <-ctx.Done():
			return ErrCredentialHandoffTimeout
		case <-time.After(25 * time.Millisecond):
		}
	}
	defer conn.Close()
	stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopConn()
	_ = conn.SetDeadline(deadline)
	if _, err = io.WriteString(conn, credentialHeader(runID)); err != nil {
		return errors.New("credential handoff failed")
	}
	if _, err = conn.Write(data); err != nil {
		return errors.New("credential handoff failed")
	}
	if err = conn.(*net.UnixConn).CloseWrite(); err != nil {
		return errors.New("credential handoff failed")
	}
	ready, err := io.ReadAll(io.LimitReader(conn, 65))
	if err != nil || string(ready) != credentialReady(runID) {
		return errors.New("credential handoff was not acknowledged")
	}
	return nil
}
