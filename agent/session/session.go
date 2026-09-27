// Package session runs one agent provider turn inside a private,
// memory-backed runtime. It owns what every detached agent workload shares:
// staging the owner's subscription credential in tmpfs, checking the pinned
// provider release, bounding the session's lifetime, enforcing a closed event
// vocabulary, and destroying the session on every exit path. Workloads supply
// a Policy and decide which items they accept.
package session

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/capture"
)

const maxCredentialBytes = 64 << 10

// Options open one session.
type Options struct {
	// RuntimeDir is the resolved tmpfs parent the session is created in.
	RuntimeDir string
	Provider   Provider
	// Executable is the provider's absolute path.
	Executable string
	// Auth is the owner's credential stream. It is read once and never kept.
	Auth io.Reader
}

// Session is a private provider home, temporary directory and working area
// inside a tmpfs runtime. Close removes all of it.
type Session struct {
	// Dir is the session root; Home holds the provider's credential.
	Dir, Home string
	// Version is the provider's self-reported, pinned version.
	Version    string
	provider   Provider
	executable string
	env        []string
}

// Bound limits a credential-bearing session to timeout. When it expires or
// ctx ends, auth is closed so an abandoned handoff cannot outlive the session.
func Bound(ctx context.Context, timeout time.Duration, auth io.Closer) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	finished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			auth.Close()
		case <-finished:
		}
	}()
	return ctx, func() { close(finished); cancel() }
}

// Open creates the session, stages the owner's credential in the provider
// home, checks the provider's pinned version and only then acknowledges a
// credential handoff. On any error nothing of the session remains.
func Open(ctx context.Context, opts Options) (s *Session, err error) {
	// The jb-review- prefix and the <provider>/auth.json layout are observed
	// by the Brine handoff features; every workload shares them.
	dir, err := os.MkdirTemp(opts.RuntimeDir, "jb-review-")
	if err != nil {
		return nil, err
	}
	s = &Session{Dir: dir, Home: filepath.Join(dir, opts.Provider.Name()), provider: opts.Provider, executable: opts.Executable}
	defer func() {
		if err != nil {
			err = errors.Join(err, s.Close())
			s = nil
		}
	}()
	for _, name := range []string{s.Home, filepath.Join(dir, "tmp")} {
		if err := os.Mkdir(name, 0700); err != nil {
			return s, err
		}
	}
	auth, err := io.ReadAll(io.LimitReader(opts.Auth, maxCredentialBytes+1))
	if err != nil || len(auth) > maxCredentialBytes || opts.Provider.CheckAuth(auth) != nil {
		clear(auth)
		if errors.Is(err, ErrCredentialHandoffTimeout) {
			return s, err
		}
		if ctx.Err() != nil {
			return s, ctx.Err()
		}
		return s, errors.New(opts.Provider.CheckAuth(nil).Error() + "; reauthenticate locally if needed")
	}
	err = os.WriteFile(filepath.Join(s.Home, opts.Provider.CredentialFile()), auth, 0600)
	clear(auth)
	if err != nil {
		return s, errors.New("cannot stage session credentials")
	}
	if !filepath.IsAbs(opts.Executable) {
		return s, errors.New("provider executable must have an absolute path")
	}
	s.env = append([]string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + filepath.Join(dir, "tmp"),
		"XDG_CONFIG_HOME=" + dir, "XDG_CACHE_HOME=" + dir, "XDG_DATA_HOME=" + dir, "XDG_STATE_HOME=" + dir, "LANG=C.UTF-8"},
		opts.Provider.Environment(s.Home)...)
	s.Version, err = opts.Provider.Version(ctx, func(args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, opts.Executable, args...)
		c.Env = s.env
		c.Dir = dir
		configureProcess(c)
		defer stopProcess(c)
		return c.Output()
	})
	if err != nil {
		return s, err
	}
	if handoff, ok := opts.Auth.(interface{ Acknowledge() error }); ok {
		if err := handoff.Acknowledge(); err != nil {
			return s, err
		}
	}
	return s, nil
}

// Mkdir creates a private directory inside the session.
func (s *Session) Mkdir(name string) (string, error) {
	dir := filepath.Join(s.Dir, name)
	return dir, os.Mkdir(dir, 0700)
}

// Close destroys the session: the credential, anything the provider refreshed
// or copied, and every working file.
func (s *Session) Close() error {
	if err := os.RemoveAll(s.Dir); err != nil {
		return errors.New("failed to remove provider runtime")
	}
	return nil
}

// Run executes one provider turn under p. The prompt is stdin; stderr is
// discarded because provider diagnostics can contain credentials. Every event
// is decoded into the closed vocabulary; the turn lifecycle is judged here and
// every item is judged by allow. Any refusal stops the provider at once.
func (s *Session) Run(ctx context.Context, p Policy, prompt string, allow func(Event) error) error {
	cmd := exec.CommandContext(ctx, s.executable, s.provider.Command(p)...)
	cmd.Dir = p.WorkDir
	cmd.Env = s.env
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Stderr = io.Discard // Provider diagnostics can contain credentials. Never forward them.
	configureProcess(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("could not start Codex")
	}
	defer stopProcess(cmd)
	completed := false
	judge := func(line []byte) error {
		e, err := s.provider.Decode(line)
		if err != nil {
			return err
		}
		switch e.Kind {
		case EventStarted:
			return nil
		case EventCompleted:
			completed = true
			return nil
		case EventFailed:
			return errors.New("Codex turn failed; check subscription authentication and model availability")
		}
		if e.Item == ItemError {
			return errors.New("Codex reported an error; nothing was published")
		}
		return allow(e)
	}
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), capture.MaxFileBytes)
	var traceErr error
	for scanner.Scan() {
		if err := judge(scanner.Bytes()); err != nil {
			traceErr = err
			stopProcess(cmd)
			break
		}
	}
	if traceErr == nil && scanner.Err() != nil {
		traceErr = errors.New("invalid or oversized Codex event stream")
		stopProcess(cmd)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if traceErr != nil {
		return traceErr
	}
	if waitErr != nil {
		return errors.New("Codex failed; check subscription authentication and model availability")
	}
	if !completed {
		return errors.New("Codex did not complete its turn")
	}
	return nil
}
