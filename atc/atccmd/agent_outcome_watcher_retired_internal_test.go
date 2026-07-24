package atccmd

import (
	"os"
	"strings"
	"testing"
)

// TestAgentOutcomeWatcherComponentIsRetired guards the v3-only cleanup: the
// legacy agent_outcome_watcher component, its outcome-diff mirror cache, and
// the outcome git flags must never be re-registered. The watcher is wired
// imperatively inside backendComponents (an integration-tier method resolving
// the main team from a live DB), so it cannot be enumerated from a light unit
// construction; this scans the wiring source directly, which is the smallest
// self-contained assertion that flips exactly when the component is removed.
func TestAgentOutcomeWatcherComponentIsRetired(t *testing.T) {
	source, err := os.ReadFile("command.go")
	if err != nil {
		t.Fatalf("read command.go: %v", err)
	}
	body := string(source)
	for _, retired := range []string{
		"ComponentAgentOutcomeWatcher",
		"buildAgentOutcomeWatcher",
		"agentOutcomeMirror",
		"AgentOutcomeGit",
	} {
		if strings.Contains(body, retired) {
			t.Fatalf("command.go still wires the retired outcome watcher via %q", retired)
		}
	}
}
