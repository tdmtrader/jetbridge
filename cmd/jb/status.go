package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/concourse/concourse/fly/rc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func reviewMCP(ctx context.Context, flags *flag.FlagSet, args []string) error {
	authFile := flags.String("auth-file", "", "owner-selected local Codex auth.json; required for submission")
	targetName := flags.String("target", "", "saved fly login target (required)")
	team := flags.String("team", "", "team name; defaults to the target's team")
	template := flags.String("template", "review", "base review template name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *targetName == "" || *template == "" {
		return errors.New("mcp requires --target and a nonempty template")
	}
	client, selectedTeam, err := platformClient(*targetName, *team)
	if err != nil {
		return err
	}
	return client.MCPServer(selectedTeam, *template, reviewclient.MCPOptions{AuthFile: *authFile}).Run(ctx, &mcp.StdioTransport{})
}

func reviewStatus(ctx context.Context, flags *flag.FlagSet, args []string, out io.Writer) error {
	targetName := flags.String("target", "", "saved fly login target (required)")
	team := flags.String("team", "", "team name; defaults to the target's team")
	template := flags.String("template", "review", "base review template name")
	number := flags.Int("run", 0, "Run number (required)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *targetName == "" || *template == "" || *number < 1 {
		return errors.New("status requires --target and a positive --run number")
	}
	client, selectedTeam, err := platformClient(*targetName, *team)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	run, err := client.Status(ctx, reviewclient.Handle{Team: selectedTeam, Template: *template, Number: *number})
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(run)
}

func platformClient(name, team string) (*reviewclient.Client, string, error) {
	target, err := rc.LoadTarget(rc.TargetName(name), false)
	if err != nil {
		return nil, "", err
	}
	if team == "" {
		team = target.Team().Name()
	}
	client, err := reviewclient.New(target.URL(), target.Client().HTTPClient())
	return client, team, err
}

func reviewResult(ctx context.Context, flags *flag.FlagSet, args []string, out io.Writer) error {
	targetName := flags.String("target", "", "saved fly login target (required)")
	team := flags.String("team", "", "team name; defaults to the target's team")
	template := flags.String("template", "review", "base review template name")
	number := flags.Int("run", 0, "Run number (required)")
	name := flags.String("result", "findings", "named result containing review.json")
	format := flags.String("format", "json", "output format: json or markdown")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *targetName == "" || *template == "" || *number < 1 || *name == "" || (*format != "json" && *format != "markdown") {
		return errors.New("result requires --target, a positive --run number, and json or markdown format")
	}
	client, selectedTeam, err := platformClient(*targetName, *team)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	report, err := client.Result(ctx, reviewclient.Handle{Team: selectedTeam, Template: *template, Number: *number}, *name)
	if err != nil {
		return err
	}
	if *format == "markdown" {
		_, err = io.WriteString(out, report.Markdown())
		return err
	}
	return json.NewEncoder(out).Encode(report)
}
