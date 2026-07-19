// platform-mcp is the platform MCP sidecar (shared contracts §3.2): serve
// mode (default) runs the MCP server; "checkpoint" mode is the checkpoint
// step's client (Task 14 / §3.2 addendum).
package main

import (
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/platformmcp"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "checkpoint" {
		os.Exit(runCheckpoint(os.Args[2:]))
	}

	cfg, err := platformmcp.ConfigFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-mcp: %s\n", err)
		os.Exit(2)
	}
	srv, err := platformmcp.NewServer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platform-mcp: %s\n", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "platform-mcp: serving MCP on %s (ticket %d)\n", cfg.ListenAddr, cfg.TicketID)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "platform-mcp: %s\n", err)
		os.Exit(1)
	}
}

// runCheckpoint is a placeholder until the checkpoint-gate task (remainder
// Task 18) lands the real client — its tests fail against this body, so it
// cannot survive that task.
func runCheckpoint([]string) int {
	fmt.Fprintln(os.Stderr, "platform-mcp: checkpoint mode lands with the checkpoint-gate task")
	return 2
}
