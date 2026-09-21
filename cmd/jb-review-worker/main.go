package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/concourse/concourse/agent/review"
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
	if len(args) > 0 && args[0] == "auth-handoff" {
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
		if err := review.SendCredentialHandoff(ctx, *socket, *runID, auth); err != nil {
			return err
		}
		fmt.Printf("{\"run_id\":%d,\"status\":\"ready\"}\n", *runID)
		return nil
	}
	if len(args) > 0 && args[0] == "input-tools" {
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
	}
	f := flag.NewFlagSet("jb-review-worker", flag.ContinueOnError)
	var opts review.WorkerOptions
	f.StringVar(&opts.Input, "input", "", "sealed input directory")
	f.StringVar(&opts.Output, "output", "", "new or empty review output directory")
	f.StringVar(&opts.RuntimeDir, "runtime-dir", "/dev/shm", "memory-backed private runtime parent (Linux tmpfs)")
	f.StringVar(&opts.Codex, "codex", "codex", "operator-installed Codex executable")
	f.StringVar(&opts.Model, "model", "", "Codex model (required)")
	f.DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "maximum credential-bearing session lifetime")
	authStdin := f.Bool("auth-stdin", false, "read owner-supplied auth.json from stdin, separately from input")
	authSocket := f.String("auth-socket", "", "accept one credential handoff at this private runtime socket")
	handoffTimeout := f.Duration("handoff-timeout", 2*time.Minute, "maximum wait for socket credential handoff")
	profile := f.String("profile", "", "optional operator-controlled review criteria file")
	runID := f.Int("run-id", 0, "durable Run ID; omit for local artifact review")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || opts.Input == "" || opts.Output == "" || opts.Model == "" || *authStdin == (*authSocket != "") {
		return errors.New("input, output, model and exactly one of --auth-stdin or --auth-socket are required")
	}
	if *runID < 0 {
		return errors.New("run-id must be positive")
	}
	if *runID > 0 {
		opts.RunID = runID
	}
	var err error
	opts.ReaderCommand, err = os.Executable()
	if err != nil {
		return err
	}
	if *profile != "" {
		opts.Profile, err = os.ReadFile(*profile)
		if err != nil {
			return err
		}
	}
	if *authSocket != "" {
		opts.Auth, err = review.ListenCredentialHandoff(ctx, opts.RuntimeDir, *authSocket, *runID, *handoffTimeout)
	} else {
		opts.Auth, err = credentialInput()
	}
	if err != nil {
		return err
	}
	_, err = review.RunWorker(ctx, opts)
	return err
}
