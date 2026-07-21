// merge-runner is the merge step's pod entrypoint (design 2026-07-20 §2):
// it reads MERGE_CONFIG, fetches, computes the prospective merge, gates the
// MERGED result, and pushes the target branch. Exit taxonomy matches
// harvest-runner: 0 ok, 1 fail (conflict, failing gate, or a target that
// moved), 2 platform error.
//
// It runs POD-SIDE. Credentials reach it only through the mounted secret,
// never through the web process — the web is deliberately stateless with
// respect to git.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/agent/merge"
	schema "github.com/concourse/concourse/agent/schema"
)

func main() {
	raw := os.Getenv("MERGE_CONFIG")
	if raw == "" {
		fmt.Fprintln(os.Stderr, "MERGE_CONFIG is not set")
		os.Exit(2)
	}

	// cfg carries the merge plan; gatePolicy rides alongside it so the same
	// v0.5 engine harvest uses can gate the merged tree.
	var cfg struct {
		merge.Config
		GatePolicy harvest.GatePolicy `json:"gate_policy"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "malformed MERGE_CONFIG: %v\n", err)
		os.Exit(2)
	}

	workspaceDir := os.Getenv("MERGE_WORKSPACE_DIR")
	if workspaceDir == "" {
		fmt.Fprintln(os.Stderr, "MERGE_WORKSPACE_DIR is not set")
		os.Exit(2)
	}

	// Git credentials: same §8.3 secret shape and well-known path harvest
	// uses. Because the target is the ticket's TargetBranch (jetbridge for
	// platform work, with CI promoting to main), this needs no more
	// privilege than harvest already holds.
	if cfg.Push {
		credsDir := os.Getenv("MERGE_CREDS_DIR")
		if credsDir == "" {
			credsDir = "/var/run/agent/git"
		}
		askpass, cleanup, err := harvest.WriteAskpass(credsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "git credentials: %v\n", err)
			os.Exit(2)
		}
		defer cleanup()
		// merge's git helper builds its env from os.Environ(), so exporting
		// here is enough — the token never appears in argv.
		os.Setenv("GIT_ASKPASS", askpass)
		os.Setenv("GIT_TERMINAL_PROMPT", "0")
	}

	gates := gateRunner(cfg.GatePolicy)

	_, code := merge.Run(cfg.Config, workspaceDir, gates, os.Stdout)
	os.Exit(code)
}

// gateRunner adapts harvest's v0.5 gate engine to merge.GateRunner. Returns
// nil when no gates are configured, which merge.Run treats as "no gating".
//
// baseSHA is deliberately empty: the v0.5 engine only enforces `full`-scope
// gates (build/test/lint), none of which consult it. An elm-build gate does,
// so it is not yet meaningful here — worth wiring when the merge step grows
// beyond full-scope gates.
func gateRunner(policy harvest.GatePolicy) merge.GateRunner {
	if len(policy.Gates) == 0 {
		return nil
	}
	events := schema.NewEventWriter(os.Stdout)
	return func(workspaceDir string) error {
		outcomes, err := harvest.RunGates(policy, workspaceDir, "", events)
		if err != nil {
			return fmt.Errorf("gate engine failure: %w", err)
		}
		for _, o := range outcomes {
			if o.Status != "ok" {
				return fmt.Errorf("gate %q %s: %s", o.Gate, o.Status, o.Detail)
			}
		}
		return nil
	}
}
