package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/concourse/concourse/agent/implement"
)

func implementCommand(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("jb implement "+args[1], flag.ContinueOnError)
	f.SetOutput(stderr)
	switch args[1] {
	case "capture":
		return implementCapture(ctx, f, args[2:], out)
	case "apply":
		return implementApply(ctx, f, args[2:], out)
	default:
		return errors.New("available implement commands: capture, apply")
	}
}

func implementCapture(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	var opts implement.CaptureOptions
	f.StringVar(&opts.Repo, "repo", ".", "local committed repository")
	f.StringVar(&opts.Base, "base", "HEAD", "base commit/ref the change is made against")
	f.StringVar(&opts.Brief, "brief", "", "local brief describing the change (required)")
	f.StringVar(&opts.Output, "output", "", "new snapshot directory outside the repository (required)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || opts.Brief == "" || opts.Output == "" {
		return errors.New("capture requires --brief and --output and takes no positional arguments")
	}
	snapshot, err := implement.CaptureSnapshot(ctx, opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(map[string]string{"input": snapshot.Dir, "input_digest": snapshot.Digest, "base_commit": snapshot.Manifest.BaseCommit})
}

func implementApply(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	var opts implement.ApplyOptions
	f.StringVar(&opts.Repo, "repo", ".", "local repository containing the change's base commit")
	f.StringVar(&opts.ResultDir, "result-dir", "", "published change directory holding change.patch and summary.json (required)")
	f.StringVar(&opts.Branch, "branch", "", "new branch name; defaults to impl/run-<n>, or impl/local-<patch digest> outside a Run")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || opts.ResultDir == "" {
		return errors.New("apply requires --result-dir and takes no positional arguments")
	}
	applied, err := implement.Apply(ctx, opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(applied)
}
