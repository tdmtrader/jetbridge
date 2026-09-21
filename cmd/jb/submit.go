package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	reviewclient "github.com/concourse/concourse/agent/review/client"
)

func reviewSubmit(ctx context.Context, flags *flag.FlagSet, args []string, out io.Writer) error {
	target := flags.String("target", "", "saved fly login target (required)")
	var options reviewclient.SubmitOptions
	flags.StringVar(&options.Team, "team", "", "team name; defaults to the target's team")
	flags.StringVar(&options.Template, "template", "review", "installed review template")
	flags.StringVar(&options.Input, "input", "", "captured input bundle directory (required)")
	flags.StringVar(&options.Receipt, "receipt", "", "local receipt outside the bundle; reuse this file when retrying (required)")
	flags.StringVar(&options.AuthFile, "auth-file", "", "owner-selected local Codex auth.json (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *target == "" || options.Input == "" || options.Receipt == "" || options.AuthFile == "" {
		return errors.New("submit requires --target, --input, --receipt and --auth-file")
	}
	client, team, err := platformClient(*target, options.Team)
	if err != nil {
		return err
	}
	options.Team = team
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
