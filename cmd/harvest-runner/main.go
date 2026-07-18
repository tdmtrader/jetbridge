// harvest-runner is the harvest step's pod entrypoint (§2.8.1): it
// reads HARVEST_CONFIG, verifies the committed workspace, and pushes
// the stable ticket branch by sha. Exit taxonomy: 0 pass, 1 fail
// (agent's workspace problem), 2 platform error. Results JSON goes to
// stdout for the build log; the exec owns all server-side effects
// (ticket transition) based on the exit code.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/harvest"
)

func main() {
	raw := os.Getenv("HARVEST_CONFIG")
	if raw == "" {
		fmt.Fprintln(os.Stderr, "HARVEST_CONFIG is not set")
		os.Exit(2)
	}
	var cfg harvest.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "malformed HARVEST_CONFIG: %v\n", err)
		os.Exit(2)
	}

	workspaceDir := os.Getenv("HARVEST_WORKSPACE_DIR")
	if workspaceDir == "" {
		fmt.Fprintln(os.Stderr, "HARVEST_WORKSPACE_DIR is not set")
		os.Exit(2)
	}

	credsDir := os.Getenv("HARVEST_CREDS_DIR")
	if credsDir == "" && cfg.Push {
		// §8.3: the git-cred secret mounts at the well-known path only
		// when push is requested; the exec sets the env to match.
		credsDir = "/var/run/agent/git"
	}

	os.Exit(harvest.Run(cfg, workspaceDir, credsDir, os.Getenv("AGENT_FLIGHT_DIR"), os.Stdout))
}
