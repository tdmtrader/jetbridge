package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/agent/review"
	"github.com/concourse/concourse/agent/session"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Closing stdin also interrupts an abandoned credential handoff.
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			os.Stdin.Close()
		case <-done:
		}
	}()
	err := run(ctx, os.Args[1:])
	close(done)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	mode := ""
	if len(args) > 0 {
		mode = args[0]
	}
	switch mode {
	case "auth-handoff":
		f := flag.NewFlagSet("auth-handoff", flag.ContinueOnError)
		socket := f.String("socket", "", "worker's private credential socket")
		runID := f.Int("run-id", 0, "authorized durable Run ID")
		timeout := f.Duration("timeout", 2*time.Minute, "maximum handoff duration")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *timeout <= 0 {
			return errors.New("auth-handoff requires a positive timeout and no positional arguments")
		}
		ctx, cancel := context.WithTimeout(ctx, *timeout)
		defer cancel()
		auth, err := credentialInput()
		if err != nil {
			return err
		}
		if err := session.SendCredentialHandoff(ctx, *socket, *runID, auth); err != nil {
			return err
		}
		fmt.Printf("{\"run_id\":%d,\"status\":\"ready\"}\n", *runID)
		return nil
	case "input-tools":
		f := flag.NewFlagSet("input-tools", flag.ContinueOnError)
		input := f.String("input", "", "sealed input directory")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *input == "" || f.NArg() != 0 {
			return errors.New("input-tools requires --input")
		}
		bundle, err := review.LoadBundle(*input)
		if err != nil {
			return err
		}
		return review.ServeInputTools(bundle, os.Stdin, os.Stdout)
	case "workspace-tools":
		f := flag.NewFlagSet("workspace-tools", flag.ContinueOnError)
		root := f.String("root", "", "the session's writable workspace")
		snapshot := f.String("snapshot", "", "sealed snapshot whose prior change and review findings are served read-only")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if *root == "" || f.NArg() != 0 {
			return errors.New("workspace-tools requires --root")
		}
		var inputs map[string][]byte
		if *snapshot != "" {
			s, err := implement.LoadSnapshot(*snapshot)
			if err != nil {
				return err
			}
			if inputs, err = s.ReadOnlyInputs(); err != nil {
				return err
			}
		}
		return implement.ServeWorkspaceTools(*root, inputs, os.Stdin, os.Stdout)
	case "implement":
		var opts implement.WorkerOptions
		f := flag.NewFlagSet("implement", flag.ContinueOnError)
		f.StringVar(&opts.Input, "input", "", "sealed snapshot directory")
		f.StringVar(&opts.Output, "output", "", "new or empty change output directory")
		common, err := sessionFlags(f, args[1:], &opts.RuntimeDir, &opts.Codex, &opts.Model, &opts.Timeout, "optional operator-controlled implementation criteria file")
		if err == nil && (opts.Input == "" || opts.Output == "") {
			err = errUsage
		}
		if err == nil {
			opts.Auth, err = common.openAuth(ctx, opts.RuntimeDir)
		}
		if err != nil {
			return err
		}
		opts.Profile, opts.RunID, opts.ToolsCommand = common.profile, common.runID, common.self
		_, err = implement.RunWorker(ctx, opts)
		return err
	default:
		var opts review.WorkerOptions
		f := flag.NewFlagSet("jb-review-worker", flag.ContinueOnError)
		f.StringVar(&opts.Input, "input", "", "sealed input directory")
		f.StringVar(&opts.Output, "output", "", "new or empty review output directory")
		common, err := sessionFlags(f, args, &opts.RuntimeDir, &opts.Codex, &opts.Model, &opts.Timeout, "optional operator-controlled review criteria file")
		if err == nil && (opts.Input == "" || opts.Output == "") {
			err = errUsage
		}
		if err == nil {
			opts.Auth, err = common.openAuth(ctx, opts.RuntimeDir)
		}
		if err != nil {
			return err
		}
		opts.Profile, opts.RunID, opts.ReaderCommand = common.profile, common.runID, common.self
		_, err = review.RunWorker(ctx, opts)
		return err
	}
}

var errUsage = errors.New("input, output, model and exactly one of --auth-stdin or --auth-socket are required")

type sessionOptions struct {
	profile []byte
	runID   *int
	// self is this executable, which Codex starts again as the private tool server.
	self           string
	authSocket     string
	handoffTimeout time.Duration
}

// sessionFlags parses the flags every credential-bearing mode shares.
func sessionFlags(f *flag.FlagSet, args []string, runtimeDir, codex, model *string, timeout *time.Duration, profileUsage string) (sessionOptions, error) {
	var out sessionOptions
	f.StringVar(runtimeDir, "runtime-dir", "/dev/shm", "memory-backed private runtime parent (Linux tmpfs)")
	f.StringVar(codex, "codex", "codex", "operator-installed Codex executable")
	f.StringVar(model, "model", "", "Codex model (required)")
	f.DurationVar(timeout, "timeout", 30*time.Minute, "maximum credential-bearing session lifetime")
	authStdin := f.Bool("auth-stdin", false, "read owner-supplied auth.json from stdin, separately from input")
	f.StringVar(&out.authSocket, "auth-socket", "", "accept one credential handoff at this private runtime socket")
	f.DurationVar(&out.handoffTimeout, "handoff-timeout", 2*time.Minute, "maximum wait for socket credential handoff")
	profile := f.String("profile", "", profileUsage)
	runID := f.Int("run-id", 0, "durable Run ID; omit for a local session")
	if err := f.Parse(args); err != nil {
		return out, err
	}
	if f.NArg() != 0 || *model == "" || *authStdin == (out.authSocket != "") {
		return out, errUsage
	}
	if *runID < 0 {
		return out, errors.New("run-id must be positive")
	}
	if *runID > 0 {
		out.runID = runID
	}
	var err error
	out.self, err = os.Executable()
	if err != nil {
		return out, err
	}
	if *profile != "" {
		out.profile, err = os.ReadFile(*profile)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// openAuth opens the credential stream: this process's stdin, or one
// handoff on a socket directly inside the private runtime.
func (o sessionOptions) openAuth(ctx context.Context, runtimeDir string) (io.ReadCloser, error) {
	if o.authSocket != "" {
		runID := 0
		if o.runID != nil {
			runID = *o.runID
		}
		return session.ListenCredentialHandoff(ctx, runtimeDir, o.authSocket, runID, o.handoffTimeout)
	}
	return credentialInput()
}
