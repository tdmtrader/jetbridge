package main

import (
	"context"
	"errors"
	"flag"

	implementclient "github.com/concourse/concourse/agent/implement/client"
	reviewclient "github.com/concourse/concourse/agent/review/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpSelection is what a local stdio MCP server is fixed to when it starts:
// the saved fly login, its team, each workload's installed template and the
// owner's auth file. Tool calls choose none of it. An empty template leaves
// that workload's tools out.
type mcpSelection struct {
	name, target, team, authFile string
	reviewTemplate               string
	implementTemplate            string
}

func (m mcpSelection) server() (*mcp.Server, error) {
	url, transport, team, err := savedLogin(m.target, m.team)
	if err != nil {
		return nil, err
	}
	s := mcp.NewServer(&mcp.Implementation{Name: m.name, Version: "1"}, nil)
	if m.reviewTemplate != "" {
		client, err := reviewclient.New(url, transport)
		if err != nil {
			return nil, err
		}
		reviewclient.RegisterTools(s, client, reviewclient.MCPOptions{Team: team, Template: m.reviewTemplate, AuthFile: m.authFile})
	}
	if m.implementTemplate != "" {
		client, err := implementclient.New(url, transport)
		if err != nil {
			return nil, err
		}
		implementclient.RegisterTools(s, client, implementclient.MCPOptions{Team: team, Template: m.implementTemplate, AuthFile: m.authFile})
	}
	return s, nil
}

// detachedMCPServer parses `jb mcp`: one server with every workload's tools.
func detachedMCPServer(flags *flag.FlagSet, args []string) (*mcp.Server, error) {
	selection := mcpSelection{name: "jetbridge"}
	flags.StringVar(&selection.authFile, "auth-file", "", "owner-selected local Codex auth.json; required for submission")
	flags.StringVar(&selection.target, "target", "", "saved fly login target (required)")
	flags.StringVar(&selection.team, "team", "", "team name; defaults to the target's team")
	flags.StringVar(&selection.reviewTemplate, "review-template", "review", "installed review template")
	flags.StringVar(&selection.implementTemplate, "implement-template", "implement", "installed implement template")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 || selection.target == "" || selection.reviewTemplate == "" || selection.implementTemplate == "" {
		return nil, errors.New("mcp requires --target and nonempty templates")
	}
	return selection.server()
}

// reviewMCPServer parses `jb review mcp`: the same server with only the review
// tools, kept so existing configurations work unchanged.
func reviewMCPServer(flags *flag.FlagSet, args []string) (*mcp.Server, error) {
	selection := mcpSelection{name: "jetbridge-review"}
	flags.StringVar(&selection.authFile, "auth-file", "", "owner-selected local Codex auth.json; required for submission")
	flags.StringVar(&selection.target, "target", "", "saved fly login target (required)")
	flags.StringVar(&selection.team, "team", "", "team name; defaults to the target's team")
	flags.StringVar(&selection.reviewTemplate, "template", "review", "base review template name")
	if err := flags.Parse(args); err != nil {
		return nil, err
	}
	if flags.NArg() != 0 || selection.target == "" || selection.reviewTemplate == "" {
		return nil, errors.New("mcp requires --target and a nonempty template")
	}
	return selection.server()
}

func serveMCP(ctx context.Context, parse func(*flag.FlagSet, []string) (*mcp.Server, error), flags *flag.FlagSet, args []string) error {
	s, err := parse(flags, args)
	if err != nil {
		return err
	}
	return s.Run(ctx, &mcp.StdioTransport{})
}
