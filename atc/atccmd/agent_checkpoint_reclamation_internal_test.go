package atccmd

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

func reclaimingCheckpointCommand() *RunCommand {
	command := configuredCheckpointCommand()
	command.AgentCheckpoints.GCInterval = 4 * time.Minute
	command.agentSnapshotDaemonClient = &jetbridge.DaemonClient{}
	return command
}

// Checkpoint capture writes durable objects that nothing else deletes. If
// composition succeeds without publishing a reclaimer, the objects accumulate
// forever, so the pass is a product of composition rather than an option.
func TestAgentCheckpointCompositionPublishesAReclaimer(t *testing.T) {
	command := reclaimingCheckpointCommand()

	if err := command.composeAgentCheckpoints(openTestDB(t)); err != nil {
		t.Fatalf("compose checkpoints: %v", err)
	}
	if command.agentCheckpointReclaimer == nil {
		t.Fatal("checkpoint composition published no reclamation pass")
	}
}

func TestAgentCheckpointReclamationRunsAsAComponent(t *testing.T) {
	command := reclaimingCheckpointCommand()
	if err := command.composeAgentCheckpoints(openTestDB(t)); err != nil {
		t.Fatalf("compose checkpoints: %v", err)
	}

	components, err := command.agentCheckpointReclamationComponents()
	if err != nil {
		t.Fatalf("checkpoint reclamation components: %v", err)
	}
	if len(components) != 1 {
		t.Fatalf("components = %d, want 1", len(components))
	}
	if components[0].Component.Name != atc.ComponentAgentCheckpointGC {
		t.Fatalf("component name = %q, want %q", components[0].Component.Name, atc.ComponentAgentCheckpointGC)
	}
	if components[0].Interval != command.AgentCheckpoints.GCInterval {
		t.Fatalf("interval = %s, want %s", components[0].Interval, command.AgentCheckpoints.GCInterval)
	}
	if components[0].Runnable == nil {
		t.Fatal("the reclamation component has nothing to run")
	}
}

func TestAgentCheckpointReclamationIsAbsentWhenCheckpointsAreDisabled(t *testing.T) {
	command := &RunCommand{}

	components, err := command.agentCheckpointReclamationComponents()
	if err != nil {
		t.Fatalf("checkpoint reclamation components: %v", err)
	}
	if len(components) != 0 {
		t.Fatalf("disabled checkpoints scheduled %d reclamation components", len(components))
	}
}

func TestAgentCheckpointGCIntervalMustBePositive(t *testing.T) {
	command := configuredCheckpointCommand()
	command.AgentCheckpoints.GCInterval = 0

	err := command.validateAgentCheckpoints()
	if err == nil {
		t.Fatal("expected a non-positive GC interval to be rejected")
	}
	if got := err.Error(); !strings.Contains(got, "--agent-checkpoint-gc-interval") {
		t.Fatalf("error = %q, want it to name the GC interval flag", got)
	}
}
