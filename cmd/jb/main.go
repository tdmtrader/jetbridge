// jb contains the artifact and remote client operations for JetBridge reviews.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/concourse/concourse/agent/review"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "review" {
		return errors.New("usage: jb review capture|submit|render|schema|status|result|mcp [options]")
	}
	f := flag.NewFlagSet("jb review "+args[1], flag.ContinueOnError)
	f.SetOutput(stderr)
	switch args[1] {
	case "submit":
		return reviewSubmit(ctx, f, args[2:], out)
	case "mcp":
		return reviewMCP(ctx, f, args[2:])
	case "result":
		return reviewResult(ctx, f, args[2:], out)
	case "status":
		return reviewStatus(ctx, f, args[2:], out)
	case "capture":
		var opts review.CaptureOptions
		f.StringVar(&opts.Repo, "repo", ".", "local committed repository")
		f.StringVar(&opts.Base, "base", "", "explicit base commit/ref (required)")
		f.StringVar(&opts.Head, "head", "HEAD", "head commit/ref")
		f.StringVar(&opts.Plan, "plan", "", "optional local plan file")
		f.StringVar(&opts.Output, "output", "", "new bundle directory outside the repository (required)")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected positional arguments")
		}
		bundle, err := review.Capture(ctx, opts)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(map[string]string{"input": bundle.Dir, "input_digest": bundle.Digest, "base_commit": bundle.Manifest.BaseCommit, "head_commit": bundle.Manifest.HeadCommit})
	case "render":
		input := f.String("input", "", "captured bundle directory")
		report := f.String("report", "", "review.json to validate and render")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *input == "" || *report == "" {
			return errors.New("input and report are required")
		}
		b, err := review.LoadBundle(*input)
		if err != nil {
			return err
		}
		file, err := os.Open(*report)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, (16<<20)+1))
		if err != nil {
			return err
		}
		r, err := review.ParseReport(data, b)
		if err != nil {
			return err
		}
		_, err = io.WriteString(out, r.Markdown())
		return err
	case "schema":
		if len(args) != 2 {
			return errors.New("schema takes no arguments")
		}
		_, err := out.Write(review.Schema())
		return err
	default:
		return errors.New("available review commands: capture, submit, render, schema, status, result, mcp")
	}
}
