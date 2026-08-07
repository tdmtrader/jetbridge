package atccmd

import (
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/worker/jetbridge"
)

func TestAgentCheckpointCompositionDisabledIsANoop(t *testing.T) {
	command := &RunCommand{}
	if err := command.composeAgentCheckpoints(nil); err != nil {
		t.Fatalf("compose disabled checkpoints: %v", err)
	}
	if command.agentCheckpointStepConfig != nil {
		t.Fatal("disabled checkpoint composition published a config")
	}
	if options, ok := command.agentCheckpointCoreStepFactoryOptions(); ok || options != nil {
		t.Fatal("disabled checkpoint composition exposed engine options")
	}
}

func TestAgentCheckpointCompositionFailsClosedWithoutSharedDependencies(t *testing.T) {
	command := configuredCheckpointCommand()
	if err := command.composeAgentCheckpoints(nil); err == nil {
		t.Fatal("expected missing database connection to fail")
	}
	if command.agentCheckpointStepConfig != nil {
		t.Fatal("missing connection published a partial checkpoint config")
	}

	if err := command.composeAgentCheckpoints(openTestDB(t)); err == nil {
		t.Fatal("expected missing shared snapshot daemon to fail")
	}
	if command.agentCheckpointStepConfig != nil {
		t.Fatal("missing daemon published a partial checkpoint config")
	}

	command.AgentSnapshots.Enabled = false
	command.agentSnapshotDaemonClient = &jetbridge.DaemonClient{}
	if err := command.composeAgentCheckpoints(openTestDB(t)); err == nil {
		t.Fatal("expected disabled snapshots to fail closed")
	}
}

func TestAgentCheckpointCompositionRetainsStaticPolicyAndIsIdempotent(t *testing.T) {
	command := configuredCheckpointCommand()
	command.agentSnapshotDaemonClient = &jetbridge.DaemonClient{}
	connection := openTestDB(t)

	if err := command.composeAgentCheckpoints(connection); err != nil {
		t.Fatal(err)
	}
	first := command.agentCheckpointStepConfig
	if first == nil || first.Factory == nil || first.RecoveryFactory == nil {
		t.Fatal("checkpoint composition did not retain execution and recovery factories")
	}
	if first.Provider != "anthropic" || first.Adapter != (provider.Identity{Name: "claude-cli", Version: "legacy-stream-json"}) {
		t.Fatalf("provider identity = %q/%#v, want static server-owned anthropic claude-cli", first.Provider, first.Adapter)
	}
	if first.ElapsedInterval != command.AgentCheckpoints.ElapsedInterval ||
		first.MaxArchiveBytes != command.AgentCheckpoints.MaxBytes ||
		first.MaxArchiveEntries != command.AgentSnapshots.MaxFiles {
		t.Fatalf("step policy = interval:%s bytes:%d entries:%d, want command bounds", first.ElapsedInterval, first.MaxArchiveBytes, first.MaxArchiveEntries)
	}
	runID := snapshot.WorkflowRunID(456)
	controller, err := first.Factory.NewAgentCheckpointExecution(checkpoint.Identity{WorkflowRunID: &runID, BuildID: 123, PlanID: "plan", FunctionID: "function"})
	if err != nil || controller == nil {
		t.Fatalf("retained factory = (%T, %v), want configured controller", controller, err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	recovery, err := first.RecoveryFactory.NewAgentCheckpointRecovery(exec.AgentCheckpointImmutableProvenance{
		Identity: checkpoint.Identity{WorkflowRunID: &runID, BuildID: 123, PlanID: "plan", FunctionID: "function"},
		Provider: "anthropic", Adapter: first.Adapter,
		RuntimeImage: "registry.example/agent@sha256:" + strings.Repeat("b", 64), Model: "model",
		ConfigDigest: digest, InputDigest: digest, MCPDigest: digest, SkillDigest: digest,
	})
	if err != nil || recovery == nil {
		t.Fatalf("retained recovery factory = (%T, %v), want configured fail-closed controller", recovery, err)
	}

	if err := command.composeAgentCheckpoints(nil); err != nil {
		t.Fatalf("recompose initialized checkpoints: %v", err)
	}
	if command.agentCheckpointStepConfig != first {
		t.Fatal("repeated checkpoint composition replaced the command-scoped config")
	}
	options, ok := command.agentCheckpointCoreStepFactoryOptions()
	if !ok || len(options) != 1 {
		t.Fatalf("checkpoint composition exposed %d engine options, want one", len(options))
	}
}

func configuredCheckpointCommand() *RunCommand {
	command := &RunCommand{}
	command.AgentSnapshots.Enabled = true
	command.AgentCheckpoints.Enabled = true
	command.AgentCheckpoints.ElapsedInterval = 3 * time.Minute
	command.AgentCheckpoints.CaptureTimeout = 7 * time.Minute
	command.AgentCheckpoints.FenceTTL = 9 * time.Minute
	command.AgentCheckpoints.MaxBytes = 12345
	command.AgentCheckpoints.MaxAttempts = 4
	command.AgentSnapshots.MaxFiles = 321
	return command
}
