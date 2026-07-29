package exec

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/runtime"
)

func TestAgentCheckpointExecutionPreparesThenMarksRunningWithFreshFences(t *testing.T) {
	store := &executionTestAttempts{}
	controller, err := NewAgentCheckpointExecution(AgentCheckpointExecutionConfig{Identity: captureIdentity(), MaxTotalAttempts: 3, FenceTTL: time.Minute}, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := controller.Prepare(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if attempt.State != checkpoint.AttemptMaterializing || attempt.ExecutionAttempt != 1 {
		t.Fatalf("attempt = %#v", attempt)
	}
	if len(store.transitions) != 1 || store.transitions[0].ExpectedState != checkpoint.AttemptScheduling {
		t.Fatalf("transitions = %#v", store.transitions)
	}
	if store.acquireCount != 1 || store.releaseCount != 1 {
		t.Fatalf("fences acquire/release = %d/%d", store.acquireCount, store.releaseCount)
	}
	attempt, err = controller.MarkRunning(context.Background())
	if err != nil || attempt.State != checkpoint.AttemptRunning {
		t.Fatalf("mark running = %#v, %v", attempt, err)
	}
	if len(store.transitions) != 2 || store.transitions[1].ExpectedState != checkpoint.AttemptMaterializing || store.acquireCount != 2 || store.releaseCount != 2 {
		t.Fatalf("running transition/fences = %#v %d/%d", store.transitions, store.acquireCount, store.releaseCount)
	}
}

func TestAgentCheckpointExecutionPrepareReentersWithoutRecoveryAllocation(t *testing.T) {
	store := &executionTestAttempts{hasCurrent: true, current: checkpoint.Attempt{Identity: captureIdentity(), ExecutionAttempt: 2, State: checkpoint.AttemptFinalizing}}
	controller, err := NewAgentCheckpointExecution(AgentCheckpointExecutionConfig{Identity: captureIdentity(), FenceTTL: time.Minute}, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := controller.Prepare(context.Background())
	if err != nil || attempt.ExecutionAttempt != 2 || attempt.State != checkpoint.AttemptFinalizing {
		t.Fatalf("prepare = %#v, %v", attempt, err)
	}
	if store.allocateCount != 0 || len(store.transitions) != 0 {
		t.Fatalf("re-entry allocated or transitioned: %d %#v", store.allocateCount, store.transitions)
	}
}

func TestAgentCheckpointExecutionMapsTypedInterruptionsExactly(t *testing.T) {
	for runtimeReason, want := range map[runtime.InterruptionReason]checkpoint.InterruptionReason{
		runtime.InterruptionPodDeleted: checkpoint.InterruptionPodDeleted,
		runtime.InterruptionEvicted:    checkpoint.InterruptionEvicted,
		runtime.InterruptionNodeLost:   checkpoint.InterruptionNodeLost,
		runtime.InterruptionPreempted:  checkpoint.InterruptionPreempted,
	} {
		t.Run(string(runtimeReason), func(t *testing.T) {
			store := &executionTestAttempts{hasCurrent: true, current: checkpoint.Attempt{Identity: captureIdentity(), ExecutionAttempt: 1, State: checkpoint.AttemptRunning}}
			controller, _ := NewAgentCheckpointExecution(AgentCheckpointExecutionConfig{Identity: captureIdentity(), FenceTTL: time.Minute}, store, nil, nil)
			if _, err := controller.MarkInterrupted(context.Background(), store.current, runtimeReason); err != nil || store.interruption.Reason != want {
				t.Fatalf("interruption = %#v, %v", store.interruption, err)
			}
		})
	}
	controller, _ := NewAgentCheckpointExecution(AgentCheckpointExecutionConfig{Identity: captureIdentity(), FenceTTL: time.Minute}, &executionTestAttempts{}, nil, nil)
	if _, err := controller.MarkInterrupted(context.Background(), checkpoint.Attempt{ExecutionAttempt: 1}, "untyped"); err == nil {
		t.Fatal("accepted an unknown runtime interruption")
	}
}

func TestAgentCheckpointExecutionCapturesOnlyFromDurableRuntimeStates(t *testing.T) {
	store := &executionTestAttempts{hasCurrent: true, current: checkpoint.Attempt{Identity: captureIdentity(), ExecutionAttempt: 1, State: checkpoint.AttemptMaterializing}}
	controller, err := NewAgentCheckpointExecution(AgentCheckpointExecutionConfig{Identity: captureIdentity(), FenceTTL: time.Minute}, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.CaptureLive(context.Background(), CheckpointCaptureTriggerElapsed, AgentCheckpointCaptureRequest{}); err == nil {
		t.Fatal("live capture advanced a materializing attempt")
	}
	store.current.State = checkpoint.AttemptRunning
	if _, err := controller.CaptureTerminal(context.Background(), AgentCheckpointCaptureRequest{}); err == nil {
		t.Fatal("terminal capture accepted a non-finalizing attempt")
	}
	if len(store.transitions) != 0 {
		t.Fatalf("capture unexpectedly changed state: %#v", store.transitions)
	}
}

func TestAgentCheckpointExecutionFinalizesDespiteReportableCaptureError(t *testing.T) {
	store := &executionTestAttempts{hasCurrent: true, current: checkpoint.Attempt{Identity: captureIdentity(), ExecutionAttempt: 1, State: checkpoint.AttemptRunning}}
	controller, err := NewAgentCheckpointExecution(AgentCheckpointExecutionConfig{Identity: captureIdentity(), FenceTTL: time.Minute}, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.FinalizeSucceeded(context.Background(), AgentCheckpointCaptureRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Attempt.State != checkpoint.AttemptSucceeded || result.CaptureError == nil {
		t.Fatalf("finalization result = %#v", result)
	}
	if len(store.transitions) != 1 || store.transitions[0].ExpectedState != checkpoint.AttemptRunning || store.acquireCount != 2 || store.releaseCount != 2 {
		t.Fatalf("finalization fences/transitions = %#v %d/%d", store.transitions, store.acquireCount, store.releaseCount)
	}
}

type executionTestAttempts struct {
	current                    checkpoint.Attempt
	hasCurrent                 bool
	allocateCount              int
	transitions                []checkpoint.TransitionAttemptRequest
	acquireCount, releaseCount int
	interruption               checkpoint.MarkAttemptInterruptedRequest
	releaseErr                 error
}

func (s *executionTestAttempts) Current(context.Context, checkpoint.Identity) (checkpoint.Attempt, bool, error) {
	return s.current, s.hasCurrent, nil
}
func (s *executionTestAttempts) AllocateInitial(_ context.Context, r checkpoint.AllocateInitialAttemptRequest) (checkpoint.Attempt, error) {
	s.allocateCount++
	s.current = checkpoint.Attempt{Identity: r.Identity, ExecutionAttempt: 1, State: checkpoint.AttemptScheduling, Current: true, MaterializationID: r.MaterializationID}
	s.hasCurrent = true
	return s.current, nil
}
func (s *executionTestAttempts) Transition(_ context.Context, r checkpoint.TransitionAttemptRequest) (checkpoint.Attempt, error) {
	s.transitions = append(s.transitions, r)
	s.current.State = r.State
	return s.current, nil
}
func (s *executionTestAttempts) AcquireFence(_ context.Context, r checkpoint.AcquireAttemptFenceRequest) (checkpoint.AttemptFence, error) {
	s.acquireCount++
	return checkpoint.AttemptFence{FenceClaim: checkpoint.FenceClaim{ExecutionAttempt: r.ExecutionAttempt, Token: r.Token}}, nil
}
func (s *executionTestAttempts) ReleaseFence(context.Context, checkpoint.ReleaseAttemptFenceRequest) error {
	s.releaseCount++
	return s.releaseErr
}
func (s *executionTestAttempts) MarkInterrupted(_ context.Context, request checkpoint.MarkAttemptInterruptedRequest) (checkpoint.Attempt, error) {
	s.interruption = request
	s.current.State = checkpoint.AttemptInterrupted
	return s.current, nil
}
func (s *executionTestAttempts) FinalizeSucceeded(context.Context, checkpoint.FinalizeSucceededRequest) (checkpoint.Attempt, error) {
	s.current.State = checkpoint.AttemptSucceeded
	return s.current, nil
}
