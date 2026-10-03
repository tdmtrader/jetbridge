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

	reviewclient "github.com/concourse/concourse/agent/review/client"
)

func reviewLand(ctx context.Context, flags *flag.FlagSet, args []string, out io.Writer) error {
	defaultState := ""
	if cache, err := os.UserCacheDir(); err == nil {
		defaultState = filepath.Join(cache, "jb", "land")
	}
	target := flags.String("target", "", "saved fly login target (required)")
	var options reviewclient.LandOptions
	flags.StringVar(&options.Team, "team", "", "team name; defaults to the target's team")
	flags.StringVar(&options.Template, "template", "review", "installed review template")
	flags.StringVar(&options.AuthFile, "auth-file", "", "owner-selected local Codex auth.json (required)")
	flags.StringVar(&options.Repo, "repo", ".", "local committed repository whose origin receives the landing")
	flags.StringVar(&options.Head, "head", "HEAD", "head commit/ref to review and land on origin core")
	flags.StringVar(&options.Policy.BlockAt, "block-at", "medium", "lowest finding severity that refuses a landing: low, medium, high or blocker")
	flags.StringVar(&options.StateDir, "state", defaultState, "directory outside the repository keeping each landing's bundle and invocation receipt; rerun with it to resume")
	timeout := flags.Duration("timeout", 40*time.Minute, "bound on the landing, including the review; a push already under way finishes under its own two-minute bound")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *target == "" || options.AuthFile == "" || options.StateDir == "" || *timeout <= 0 {
		return errors.New("land requires --target, --auth-file, --state and a positive --timeout")
	}
	client, team, err := platformClient(*target, options.Team)
	if err != nil {
		return err
	}
	options.Team = team
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	outcome, err := reviewclient.Land(ctx, client, options)
	if outcome.Outcome != "" || outcome.RunNumber > 0 {
		if err != nil && outcome.Outcome == "" {
			outcome.Message = err.Error()
		}
		if encodeErr := json.NewEncoder(out).Encode(outcome); encodeErr != nil {
			return encodeErr
		}
	}
	return err
}
