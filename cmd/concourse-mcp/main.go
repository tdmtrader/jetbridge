package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/concourse/concourse/cmd/concourse-mcp/mcpserver"
	"github.com/concourse/concourse/fly/rc"
)

func main() {
	target := os.Getenv("CONCOURSE_TARGET")
	if target == "" {
		if len(os.Args) > 1 {
			target = os.Args[1]
		}
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "usage: concourse-mcp <target>")
		fmt.Fprintln(os.Stderr, "  or set CONCOURSE_TARGET env var")
		os.Exit(1)
	}

	flyTarget, err := rc.LoadTarget(rc.TargetName(target), false)
	if err != nil {
		log.Fatalf("failed to load fly target %q: %v", target, err)
	}

	client := flyTarget.Client()
	team := flyTarget.Team()

	info, err := client.GetInfo()
	if err != nil {
		log.Fatalf("failed to connect to Concourse at %s: %v", flyTarget.URL(), err)
	}
	fmt.Fprintf(os.Stderr, "concourse-mcp: connected to %s (version %s)\n", flyTarget.URL(), info.Version)

	server := mcpserver.New(client, team, flyTarget.URL())
	if err := server.Run(context.Background()); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
