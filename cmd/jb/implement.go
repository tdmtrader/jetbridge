package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/agent/implement"
	implementclient "github.com/concourse/concourse/agent/implement/client"
)

func implementCommand(ctx context.Context, args []string, out, stderr io.Writer) error {
	f := flag.NewFlagSet("jb implement "+args[1], flag.ContinueOnError)
	f.SetOutput(stderr)
	switch args[1] {
	case "capture":
		return implementCapture(ctx, f, args[2:], out)
	case "submit":
		return implementSubmit(ctx, f, args[2:], out)
	case "status":
		return implementStatus(ctx, f, args[2:], out)
	case "result":
		return implementResult(ctx, f, args[2:], out)
	case "apply":
		return implementApply(ctx, f, args[2:], out)
	default:
		return errors.New("available implement commands: capture, submit, status, result, apply")
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

// implementDestination is the platform selection every remote implement
// command shares: a saved fly login, its team and the installed template.
type implementDestination struct{ target, team, template string }

func (d *implementDestination) flags(f *flag.FlagSet) {
	f.StringVar(&d.target, "target", "", "saved fly login target (required)")
	f.StringVar(&d.team, "team", "", "team name; defaults to the target's team")
	f.StringVar(&d.template, "template", "implement", "installed implement template")
}

func (d implementDestination) client() (*implementclient.Client, string, error) {
	url, transport, team, err := savedLogin(d.target, d.team)
	if err != nil {
		return nil, "", err
	}
	client, err := implementclient.New(url, transport)
	return client, team, err
}

func implementSubmit(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	var destination implementDestination
	destination.flags(f)
	var options implementclient.SubmitOptions
	f.StringVar(&options.Input, "input", "", "captured snapshot directory (required)")
	f.StringVar(&options.Receipt, "receipt", "", "local receipt outside the snapshot; reuse this file when retrying (required)")
	f.StringVar(&options.AuthFile, "auth-file", "", "owner-selected local Codex auth.json (required)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || destination.target == "" || destination.template == "" || options.Input == "" || options.Receipt == "" || options.AuthFile == "" {
		return errors.New("submit requires --target, --input, --receipt and --auth-file")
	}
	client, team, err := destination.client()
	if err != nil {
		return err
	}
	options.Team, options.Template = team, destination.template
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := client.Submit(ctx, options)
	if result.RunID > 0 {
		if err != nil {
			result.Message = err.Error()
		}
		if encodeErr := json.NewEncoder(out).Encode(result); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}

func implementStatus(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	var destination implementDestination
	destination.flags(f)
	number := f.Int("run", 0, "Run number (required)")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || destination.target == "" || destination.template == "" || *number < 1 {
		return errors.New("status requires --target and a positive --run number")
	}
	client, team, err := destination.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	run, err := client.Status(ctx, implementclient.Handle{Team: team, Template: destination.template, Number: *number})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(run)
}

func implementResult(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	var destination implementDestination
	destination.flags(f)
	number := f.Int("run", 0, "Run number (required)")
	name := f.String("result", implementclient.ChangeResult, "named result containing change.patch and summary.json")
	output := f.String("output", "", "optional new directory to write the verified change.patch and summary.json into")
	format := f.String("format", "json", "output format: json or markdown")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || destination.target == "" || destination.template == "" || *number < 1 || *name == "" || (*format != "json" && *format != "markdown") {
		return errors.New("result requires --target, a positive --run number, and json or markdown format")
	}
	change, err := fetchChange(ctx, destination, *number, *name)
	if err != nil {
		return err
	}
	if *output != "" {
		if err := change.WriteDir(*output); err != nil {
			return err
		}
	}
	if *format == "markdown" {
		_, err = io.WriteString(out, change.Markdown())
		return err
	}
	return json.NewEncoder(out).Encode(change)
}

func fetchChange(ctx context.Context, destination implementDestination, number int, name string) (*implementclient.Change, error) {
	client, team, err := destination.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return client.Result(ctx, implementclient.Handle{Team: team, Template: destination.template, Number: number}, name)
}

func implementApply(ctx context.Context, f *flag.FlagSet, args []string, out io.Writer) error {
	var opts implement.ApplyOptions
	var destination implementDestination
	destination.flags(f)
	number := f.Int("run", 0, "Run number whose verified change to fetch and apply")
	f.StringVar(&opts.Repo, "repo", ".", "local repository containing the change's base commit")
	f.StringVar(&opts.ResultDir, "result-dir", "", "published change directory holding change.patch and summary.json")
	f.StringVar(&opts.Branch, "branch", "", "new branch name; defaults to impl/run-<Run ID>, or impl/local-<patch digest> outside a Run")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*number > 0) == (opts.ResultDir != "") || *number < 0 {
		return errors.New("apply requires exactly one of --run or --result-dir and takes no positional arguments")
	}
	if *number > 0 {
		if destination.target == "" || destination.template == "" {
			return errors.New("apply --run requires --target")
		}
		// The change is verified against its Run, written to a private
		// directory, and then read and verified again by Apply itself.
		change, err := fetchChange(ctx, destination, *number, implementclient.ChangeResult)
		if err != nil {
			return err
		}
		scratch, err := os.MkdirTemp("", "jb-implement-result-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(scratch)
		opts.ResultDir = filepath.Join(scratch, "change")
		if err := change.WriteDir(opts.ResultDir); err != nil {
			return err
		}
	}
	applied, err := implement.Apply(ctx, opts)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(applied)
}
