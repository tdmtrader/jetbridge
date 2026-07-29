package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/provider"
)

func TestRecoveryNoHeadUnsafeJournalManualizesBeforeAllocation(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	attempts := &recoveryAttemptFake{current: checkpoint.Attempt{
		Identity: provenance.Identity, ExecutionAttempt: 1, State: checkpoint.AttemptInterrupted,
		InterruptionReason: checkpoint.InterruptionEvicted,
	}}
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{}, recoveryJournalFake{effects: []checkpoint.Effect{{
		ToolCallID: "write", ToolName: "write", Provider: provenance.Provider, AdapterVersion: provenance.Adapter.Version,
		State: checkpoint.EffectBegun,
	}}})
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !launch.ManualReview || attempts.beginCalls != 0 || len(attempts.manual) != 1 {
		t.Fatalf("unsafe no-head recovery = %#v, begin=%d manual=%#v", launch, attempts.beginCalls, attempts.manual)
	}
}

func TestRecoveryNoHeadCleanJournalAllocatesCheckpointZero(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	attempts := recoveryInterruptedAttempt(provenance)
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{}, recoveryJournalFake{})
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.ManualReview || launch.Attempt.Mode != checkpoint.FallbackCheckpointZero || launch.Restore != nil || attempts.beginCalls != 1 {
		t.Fatalf("clean no-head launch = %#v, begin=%d", launch, attempts.beginCalls)
	}
}

func TestRecoveryLatestProvenanceMismatchManualizesSourceAndUsesWorkflowValueEquality(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	manifest := recoveryManifest(t, provenance, 11, 1, 1)
	workflow := *provenance.Identity.WorkflowRunID
	manifest.WorkflowRunID = &workflow // distinct pointer, same durable workflow identity
	attempts := recoveryInterruptedAttempt(provenance)
	controller := recoveryTestControllerWithAuthority(t, provenance, attempts, recoverySourceFake{latest: manifest, found: true}, recoveryJournalFake{}, recoveryNativeAuthority(provenance))
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.ManualReview || attempts.beginCalls != 1 {
		t.Fatalf("value-equal workflow identity was rejected: %#v", launch)
	}

	manifest.Provider = "other"
	attempts = recoveryInterruptedAttempt(provenance)
	controller = recoveryTestControllerWithAuthority(t, provenance, attempts, recoverySourceFake{latest: manifest, found: true}, recoveryJournalFake{}, recoveryNativeAuthority(provenance))
	launch, err = controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !launch.ManualReview || attempts.beginCalls != 0 {
		t.Fatalf("mismatched provenance did not manualize source: %#v", launch)
	}
}

func TestRecoveryOldSourceDowngradesToWorkspaceAndStripsSession(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	manifest := recoveryManifest(t, provenance, 12, 1, 1)
	manifest.SessionID = "session-old"
	attempts := recoveryInterruptedAttempt(provenance)
	attempts.current.ExecutionAttempt = 2
	controller := recoveryTestControllerWithAuthority(t, provenance, attempts, recoverySourceFake{latest: manifest, found: true}, recoveryJournalFake{}, recoveryNativeAuthority(provenance))
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.Attempt.Mode != checkpoint.FallbackWorkspaceOnly || strings.Contains(launch.RecoverySpec, "session_id") || launch.Restore == nil {
		t.Fatalf("old source recovery = %#v", launch)
	}
}

func TestRecoveryUsesCompleteJournalBeyondCheckpointEffectSnapshot(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	manifest := recoveryManifest(t, provenance, 14, 1, 1)
	attempts := recoveryInterruptedAttempt(provenance)
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{latest: manifest, found: true}, recoveryJournalFake{
		effects: []checkpoint.Effect{{
			ToolCallID: "read-after-checkpoint", ToolName: "read", Provider: provenance.Provider,
			AdapterVersion: provenance.Adapter.Version, ReadOnly: true, State: checkpoint.EffectCommitted,
		}},
	})
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.ManualReview || launch.Attempt.Mode != checkpoint.FallbackWorkspaceOnly || launch.Restore == nil {
		t.Fatalf("complete safe journal suffix was not recoverable: %#v", launch)
	}
}

func TestRecoveryFrozenWorkspaceDoesNotUpgradeWithNativeAuthority(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	manifest := recoveryManifest(t, provenance, 13, 3, 1)
	manifest.SessionID = "session-1"
	id := manifest.CheckpointID
	attempts := &recoveryAttemptFake{current: checkpoint.Attempt{Identity: provenance.Identity, ExecutionAttempt: 2, SourceExecutionAttempt: 1, SourceCheckpointID: &id, SourceCheckpointGeneration: manifest.Generation, Mode: checkpoint.FallbackWorkspaceOnly, MaterializationID: "recovery-materialization", State: checkpoint.AttemptScheduling}}
	controller := recoveryTestControllerWithAuthority(t, provenance, attempts, recoverySourceFake{retained: manifest}, recoveryJournalFake{}, recoveryNativeAuthority(provenance))
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if launch.ManualReview || launch.Attempt.Mode != checkpoint.FallbackWorkspaceOnly || strings.Contains(launch.RecoverySpec, "session_id") {
		t.Fatalf("frozen workspace recovery upgraded: %#v", launch)
	}
}

func TestRecoveryAttemptLimitManualizesSource(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	attempts := recoveryInterruptedAttempt(provenance)
	attempts.beginErr = checkpoint.ErrAttemptLimit
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{}, recoveryJournalFake{})
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !launch.ManualReview || len(attempts.manual) != 1 {
		t.Fatalf("attempt limit did not manualize: %#v", launch)
	}
}

func TestRecoveryRetainedSourceFailureManualizesExactReplacement(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	id := int64(17)
	attempts := &recoveryAttemptFake{current: checkpoint.Attempt{
		Identity: provenance.Identity, ExecutionAttempt: 2, SourceExecutionAttempt: 1,
		SourceCheckpointID: &id, SourceCheckpointGeneration: 3, Mode: checkpoint.FallbackWorkspaceOnly,
		MaterializationID: "recovery-materialization", State: checkpoint.AttemptScheduling,
	}}
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{retainedErr: errors.New("gone")}, recoveryJournalFake{})
	launch, err := controller.PrepareLaunch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !launch.ManualReview || len(attempts.manual) != 1 || attempts.manual[0].MaterializationID != "recovery-materialization" || attempts.manual[0].ExpectedState != checkpoint.AttemptScheduling {
		t.Fatalf("retained source failure = %#v manual=%#v", launch, attempts.manual)
	}
}

func TestRecoveryOrdinaryAttemptIsNotTreatedAsRecovery(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	attempts := &recoveryAttemptFake{current: checkpoint.Attempt{
		Identity: provenance.Identity, ExecutionAttempt: 1, State: checkpoint.AttemptScheduling,
	}}
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{}, recoveryJournalFake{})

	_, err := controller.PrepareLaunch(context.Background())
	if !errors.Is(err, ErrAgentCheckpointNotRecovery) {
		t.Fatalf("ordinary attempt error = %v, want %v", err, ErrAgentCheckpointNotRecovery)
	}
}

func TestRecoveryMaterializationFailureUsesCurrentPreLaunchState(t *testing.T) {
	provenance := recoveryTestProvenance(t)
	id := int64(19)
	attempts := &recoveryAttemptFake{current: checkpoint.Attempt{
		Identity: provenance.Identity, ExecutionAttempt: 2, SourceExecutionAttempt: 1,
		SourceCheckpointID: &id, SourceCheckpointGeneration: 3, Mode: checkpoint.FallbackWorkspaceOnly,
		MaterializationID: "recovery-materialization", State: checkpoint.AttemptMaterializing,
	}}
	controller := recoveryTestController(t, provenance, attempts, recoverySourceFake{}, recoveryJournalFake{})
	launch := AgentCheckpointRecoveryLaunch{Attempt: attempts.current}
	launch.Attempt.State = checkpoint.AttemptScheduling

	_, err := controller.MarkMaterializationFailed(context.Background(), launch)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts.manual) != 1 ||
		attempts.manual[0].ExpectedState != checkpoint.AttemptMaterializing ||
		attempts.manual[0].MaterializationID != "recovery-materialization" {
		t.Fatalf("materialization failure request = %#v", attempts.manual)
	}
}

func recoveryTestProvenance(t *testing.T) AgentCheckpointImmutableProvenance {
	t.Helper()
	request := validAgentCheckpointProvenanceRequest()
	provenance, err := deriveAgentCheckpointImmutableProvenance(agentCheckpointImmutableProvenanceRequest{
		PlanID: request.PlanID, Plan: request.Plan, Metadata: request.Metadata, RuntimeImage: request.RuntimeImage,
		Provider: request.Provider, Adapter: request.Adapter, Inputs: request.Inputs, Sidecars: request.Sidecars,
	})
	if err != nil {
		t.Fatal(err)
	}
	return provenance
}

func recoveryTestController(t *testing.T, provenance AgentCheckpointImmutableProvenance, attempts *recoveryAttemptFake, sources recoverySourceFake, journal recoveryJournalFake) *AgentCheckpointRecoveryController {
	return recoveryTestControllerWithAuthority(t, provenance, attempts, sources, journal, AgentCheckpointRecoveryAuthority{Provider: provenance.Provider, Adapter: provenance.Adapter, RuntimeImage: provenance.RuntimeImage, CompleteEffectJournal: true, Capabilities: checkpoint.Capabilities{SafeBoundary: true, EffectJournal: true, Version: provenance.Adapter.Version}})
}

func recoveryTestControllerWithAuthority(t *testing.T, provenance AgentCheckpointImmutableProvenance, attempts *recoveryAttemptFake, sources recoverySourceFake, journal recoveryJournalFake, authority AgentCheckpointRecoveryAuthority) *AgentCheckpointRecoveryController {
	t.Helper()
	controller, err := NewAgentCheckpointRecoveryController(AgentCheckpointRecoveryConfig{
		Provenance: provenance, MaxArchiveBytes: 1024, MaxArchiveEntries: 16,
		Authorities: []AgentCheckpointRecoveryAuthority{authority},
	}, attempts, sources, journal)
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

type recoveryAttemptFake struct {
	current    checkpoint.Attempt
	beginCalls int
	beginErr   error
	manual     []checkpoint.MarkAttemptManualReviewRequest
}

func (fake *recoveryAttemptFake) Current(context.Context, checkpoint.Identity) (checkpoint.Attempt, bool, error) {
	return fake.current, true, nil
}
func (fake *recoveryAttemptFake) BeginRecovery(_ context.Context, request checkpoint.BeginRecoveryRequest) (checkpoint.Attempt, error) {
	fake.beginCalls++
	if fake.beginErr != nil {
		return checkpoint.Attempt{}, fake.beginErr
	}
	return checkpoint.Attempt{Identity: request.Identity, ExecutionAttempt: fake.current.ExecutionAttempt + 1, SourceExecutionAttempt: request.SourceExecutionAttempt, SourceCheckpointID: request.SourceCheckpointID, SourceCheckpointGeneration: request.SourceCheckpointGeneration, Mode: request.Mode, MaterializationID: request.MaterializationID, State: checkpoint.AttemptScheduling}, nil
}
func (fake *recoveryAttemptFake) MarkManualReview(_ context.Context, request checkpoint.MarkAttemptManualReviewRequest) (checkpoint.Attempt, error) {
	fake.manual = append(fake.manual, request)
	result := fake.current
	result.State = checkpoint.AttemptManualReview
	return result, nil
}

type recoverySourceFake struct {
	latest      checkpoint.Manifest
	found       bool
	retained    checkpoint.Manifest
	retainedErr error
}

func (fake recoverySourceFake) Latest(context.Context, checkpoint.Identity) (checkpoint.Manifest, bool, error) {
	return fake.latest, fake.found, nil
}
func (fake recoverySourceFake) LoadRetainedRecoverySource(context.Context, checkpoint.Identity, int64, int) (checkpoint.Manifest, error) {
	return fake.retained, fake.retainedErr
}

type recoveryJournalFake struct {
	effects []checkpoint.Effect
	err     error
}

func (fake recoveryJournalFake) ListEffects(context.Context, checkpoint.Identity, int) ([]checkpoint.Effect, error) {
	return fake.effects, fake.err
}

func recoveryInterruptedAttempt(provenance AgentCheckpointImmutableProvenance) *recoveryAttemptFake {
	return &recoveryAttemptFake{current: checkpoint.Attempt{Identity: provenance.Identity, ExecutionAttempt: 1, State: checkpoint.AttemptInterrupted, InterruptionReason: checkpoint.InterruptionEvicted}}
}

func recoveryManifest(t *testing.T, provenance AgentCheckpointImmutableProvenance, id int64, generation, executionAttempt int) checkpoint.Manifest {
	t.Helper()
	digest := hangar.Digest("sha256:" + strings.Repeat("a", 64))
	key, err := hangar.Key(hangar.KindCheckpoint, digest)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint.Manifest{Version: 1, CheckpointID: id, Generation: generation, ExecutionAttempt: executionAttempt, WorkflowRunID: provenance.Identity.WorkflowRunID, BuildID: provenance.Identity.BuildID, PlanID: provenance.Identity.PlanID, FunctionID: provenance.Identity.FunctionID, Provider: provenance.Provider, RuntimeImage: provenance.RuntimeImage, Model: provenance.Model, ConfigDigest: provenance.ConfigDigest, InputDigest: provenance.InputDigest, MCPDigest: provenance.MCPDigest, SkillDigest: provenance.SkillDigest, Archive: &hangar.ObjectRef{Kind: hangar.KindCheckpoint, Digest: digest, Key: key, Generation: 1}, SafeAt: time.Now().UTC()}
}

func recoveryNativeAuthority(provenance AgentCheckpointImmutableProvenance) AgentCheckpointRecoveryAuthority {
	return AgentCheckpointRecoveryAuthority{Provider: provenance.Provider, Adapter: provenance.Adapter, RuntimeImage: provenance.RuntimeImage, CompleteEffectJournal: true, Capabilities: checkpoint.Capabilities{SafeBoundary: true, EffectJournal: true, SessionExport: true, NativeResume: true, Version: provenance.Adapter.Version}, RecoveryProof: &provider.RecoveryProof{Adapter: provenance.Adapter, Executable: "runner", SessionFormat: "json", SafeBoundary: true, EffectJournal: true, SessionExport: true, NativeResume: true}}
}
