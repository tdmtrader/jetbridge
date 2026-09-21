package steps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/agent/review"
)

func ReviewWorkerDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputRuntime, RunOutputRuntime]("the review image user can write its reserved output", func(in RunOutputRuntime, _ brine.Params, _ *brine.Recorder) (RunOutputRuntime, error) {
			return in, checkReviewImageOutputUser(in)
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("the review worker receives model output {string}", func(in ReviewChange, p brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			mode, _ := p.GetString(0)
			return in.runWorker(mode, reviewSyntheticAuth, "")
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("the review worker receives credentials {string}", func(in ReviewChange, p brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			kind, _ := p.GetString(0)
			auth, ok := map[string]string{"api-key": `{"OPENAI_API_KEY":"synthetic-key"}`, "empty": `{}`, "invalid-json": `invalid`}[kind]
			if !ok {
				return in, fmt.Errorf("unknown credential case %q", kind)
			}
			return in.runWorker("valid", auth, "")
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("a second review receives no subscription tokens", func(in ReviewChange, _ brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			if err := reviewNoCredentials(in); err != nil {
				return in, err
			}
			in.Output = filepath.Join(in.Workspace.Root, "second-report")
			return in.runWorker("valid", `{}`, "")
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("the review credential pipe is left open without delivering credentials", func(in ReviewChange, _ brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			return in.runWorker("valid", "", "abandoned-handoff")
		}),
		brine.DefineMap[ReviewChange, ReviewChange]("the review worker is terminated after the model refreshes credentials", func(in ReviewChange, _ brine.Params, _ *brine.Recorder) (ReviewChange, error) {
			return in.runWorker("timeout", reviewSyntheticAuth, "terminate")
		}),
		check[ReviewChange]("the published review has verdict {string}", func(in ReviewChange, p brine.Params) error {
			want, _ := p.GetString(0)
			if in.CommandErr != nil {
				return fmt.Errorf("worker failed: %w: %s", in.CommandErr, in.Stderr)
			}
			bundle, err := in.bundle()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(filepath.Join(in.Output, "review.json"))
			if err != nil {
				return err
			}
			report, err := review.ParseReport(data, bundle)
			if err != nil {
				return err
			}
			if report.Verdict != want {
				return fmt.Errorf("review verdict is %q, want %q", report.Verdict, want)
			}
			if report.Provenance.ExecutionPolicy != "inspect-only" || !strings.Contains(strings.Join(report.Assessment.Limitations, " "), "not executed") {
				return errors.New("report lacks non-execution disclosure")
			}
			if in.Mode == "finding" || in.Mode == "deletion-finding" {
				if len(report.Assessment.Findings) != 1 {
					return errors.New("seeded defect did not yield one located finding")
				}
				f := report.Assessment.Findings[0]
				side, path, line := "head", "parser.go", 2
				if in.Mode == "deletion-finding" {
					side, path, line = "base", "deleted.txt", 1
				}
				if f.ID != "f-001" || f.Location.Side != side || f.Location.Path != path || f.Location.StartLine != line {
					return errors.New("finding lost its canonical identity or location")
				}
			}
			markdown, err := os.ReadFile(filepath.Join(in.Output, "review.md"))
			if err != nil {
				return err
			}
			rendered, stderr, err := reviewCommand(in.Binaries.CLI, []string{"review", "render", "--input", in.Input, "--report", filepath.Join(in.Output, "review.json")}, "")
			if err != nil {
				return fmt.Errorf("render persisted report: %w: %s", err, stderr)
			}
			if !bytes.Equal(markdown, rendered) {
				return errors.New("published Markdown differs from a fresh CLI rendering")
			}
			return nil
		}),
		CheckThat[ReviewChange]("the review worker fails without publishing a report", func(in ReviewChange) error {
			if in.CommandErr == nil {
				return errors.New("worker accepted invalid output or credentials")
			}
			if len(in.Stderr) == 0 {
				return errors.New("worker failed without a diagnostic")
			}
			return reviewAbsent(in.Output)
		}),
		CheckThat[ReviewChange]("the review session leaves no credential files or credential-bearing output", reviewNoCredentials),
		CheckThat[ReviewChange]("the existing review is protected from a second invocation", func(in ReviewChange) error {
			beforeJSON, err := os.ReadFile(filepath.Join(in.Output, "review.json"))
			if err != nil {
				return err
			}
			beforeMD, err := os.ReadFile(filepath.Join(in.Output, "review.md"))
			if err != nil {
				return err
			}
			after, err := in.runWorker("valid", reviewSyntheticAuth, "")
			if err != nil {
				return err
			}
			if after.CommandErr == nil {
				return errors.New("worker accepted an occupied report destination")
			}
			afterJSON, err := os.ReadFile(filepath.Join(in.Output, "review.json"))
			if err != nil {
				return err
			}
			afterMD, err := os.ReadFile(filepath.Join(in.Output, "review.md"))
			if err != nil {
				return err
			}
			if !bytes.Equal(beforeJSON, afterJSON) || !bytes.Equal(beforeMD, afterMD) {
				return errors.New("second invocation changed an existing report")
			}
			return reviewNoCredentials(after)
		}),
		CheckThat[ReviewChange]("the captured review input is unchanged", func(in ReviewChange) error { _, err := in.bundle(); return err }),
	}
}

func (in ReviewChange) runWorker(mode, auth, control string) (ReviewChange, error) {
	if _, err := in.bundle(); err != nil {
		return in, err
	}
	if err := in.memoryRuntime(); err != nil {
		return in, err
	}
	in.Mode = mode
	timeout := "10s"
	if control == "abandoned-handoff" || (mode == "timeout" && control != "terminate") {
		timeout = "500ms"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, in.Binaries.Worker,
		"--input", in.Input, "--output", in.Output, "--runtime-dir", in.Workspace.Runtime,
		"--codex", in.Binaries.Provider, "--model", mode, "--timeout", timeout, "--auth-stdin")
	if in.RunID > 0 {
		cmd.Args = append(cmd.Args, "--run-id", strconv.Itoa(in.RunID))
	}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "OPENAI_API_KEY=") && !strings.HasPrefix(v, "CODEX_API_KEY=") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "OPENAI_API_KEY=synthetic-must-not-inherit", "CODEX_API_KEY=synthetic-must-not-inherit")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if control == "abandoned-handoff" {
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return in, err
		}
		defer pipe.Close()
	} else {
		cmd.Stdin = strings.NewReader(auth)
	}
	if err := cmd.Start(); err != nil {
		return in, err
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if control == "terminate" {
		// Wait on actual refreshed bytes, not a guessed delay or a call counter.
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		ready := false
		for !ready {
			select {
			case err := <-done:
				return in, fmt.Errorf("worker exited before refresh: %v", err)
			case <-ctx.Done():
				return in, errors.New("model never produced refreshed credentials")
			case <-ticker.C:
				files, err := filepath.Glob(filepath.Join(in.Workspace.Runtime, "*", "codex", "auth.json.tmp"))
				if err != nil {
					return in, err
				}
				for _, path := range files {
					data, err := os.ReadFile(path)
					if err == nil && string(data) == "synthetic-temporary" {
						ready = true
					}
				}
			}
		}
		if err := reviewTerminate(cmd); err != nil {
			return in, err
		}
	}
	in.CommandErr = <-done
	if ctx.Err() != nil {
		return in, errors.New("review command exceeded the test deadline")
	}
	in.Stdout, in.Stderr = stdout.Bytes(), stderr.Bytes()
	return in, nil
}

// Exercise the configured image UID against the real daemon's reservation. The
// process enters the directory before dropping privilege, as a volume mount
// does, so unrelated fixture ancestor permissions cannot cause the result.
func checkReviewImageOutputUser(in RunOutputRuntime) error {
	if _, err := in.prepare(); err != nil {
		return err
	}
	source, err := in.readSource()
	if err != nil {
		return err
	}
	dockerfile, err := os.ReadFile(filepath.Join(repoRoot(), "deploy", "Dockerfile.review-worker"))
	if err != nil {
		return err
	}
	user := ""
	for _, line := range strings.Split(string(dockerfile), "\n") {
		if strings.HasPrefix(line, "FROM ") {
			user = "0:0"
		}
		if strings.HasPrefix(line, "USER ") {
			user = strings.TrimSpace(strings.TrimPrefix(line, "USER "))
		}
	}
	ids := strings.Split(user, ":")
	if len(ids) != 2 {
		return fmt.Errorf("review image needs an explicit numeric UID:GID")
	}
	for _, id := range ids {
		if _, err := strconv.ParseUint(id, 10, 32); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reservation := filepath.Join(in.Start.Daemon.Output.Root, "steps", source.Source.Directory)
	before, err := os.Stat(reservation)
	if err != nil {
		return err
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("reservation ownership is unreadable")
	}
	const publish = "umask 077; mkdir report; printf complete > report/review.json; mv report/review.json .; rmdir report"
	command := exec.CommandContext(ctx, "setpriv", "--reuid="+ids[0], "--regid="+ids[1], "--clear-groups", "/bin/sh", "-ec", publish)
	if ids[0] == "0" && os.Geteuid() != 0 {
		// The output daemon runs as root, so in production root owns every
		// reservation, and an image running as root is that owner. This
		// tier runs its daemon unprivileged, inside a user namespace that
		// maps no root, so the reservation's owner here is the daemon's own
		// UID: the image user is exercised as the owner it is, not as a root
		// this namespace cannot become.
		if int(owner.Uid) != os.Geteuid() {
			return fmt.Errorf("reservation is owned by %d, not the daemon's %d", owner.Uid, os.Geteuid())
		}
		command = exec.CommandContext(ctx, "/bin/sh", "-ec", publish)
	}
	command.Dir = reservation
	if data, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("review image user %s cannot publish to the daemon reservation: %w: %s", user, err, data)
	}
	after, err := os.Stat(reservation)
	if err != nil {
		return err
	}
	if after.Mode() != before.Mode() || after.Sys().(*syscall.Stat_t).Uid != owner.Uid {
		return fmt.Errorf("publishing changed the reservation's permissions: %v -> %v", before.Mode(), after.Mode())
	}
	data, err := os.ReadFile(filepath.Join(command.Dir, "review.json"))
	if err != nil {
		return err
	}
	if string(data) != "complete" {
		return errors.New("image user failed to publish its output")
	}
	return nil
}
