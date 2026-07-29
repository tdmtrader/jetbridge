package exec

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/runtime"
)

func TestAgentCheckpointLiveIntentsRequireBothRuntimeSafeSeams(t *testing.T) {
	controller := &checkpointIntentController{}
	process := checkpointIntentProcess{}
	intents := startAgentCheckpointLiveIntents(context.Background(), controller, process, AgentCheckpointCaptureRequest{}, time.Hour, nil, nil)
	if intents != nil {
		t.Fatal("started live checkpoint intents without an exact safe-boundary and quiescence process")
	}
	if controller.liveCaptureCount != 0 {
		t.Fatalf("live capture count = %d", controller.liveCaptureCount)
	}
}

func TestAgentCheckpointLiveIntentsCoalescePreemptionThroughSafeRuntimeSeams(t *testing.T) {
	preempt := make(chan time.Time, 1)
	process := checkpointIntentSafeProcess{preemption: preempt}
	controller := &checkpointIntentController{captured: make(chan AgentCheckpointCaptureRequest, 1)}
	intents := startAgentCheckpointLiveIntents(context.Background(), controller, &process, AgentCheckpointCaptureRequest{MaxArchiveBytes: 123}, time.Hour, nil, nil)
	if intents == nil {
		t.Fatal("did not start live checkpoint intents for an exact safe process")
	}
	defer intents.Stop()

	preempt <- time.Now()
	select {
	case capture := <-controller.captured:
		if capture.Trigger != CheckpointCaptureTriggerPreemption {
			t.Fatalf("trigger = %q", capture.Trigger)
		}
		if capture.Boundary != &process || capture.Quiescence != &process || capture.MaxArchiveBytes != 123 {
			t.Fatalf("capture did not retain exact runtime authority: %#v", capture)
		}
	case <-time.After(time.Second):
		t.Fatal("preemption intent did not reach live capture")
	}
}

func TestAgentCheckpointLiveIntentsSendsElapsedOnlyThroughSafeRuntimeSeams(t *testing.T) {
	process := checkpointIntentSafeProcess{preemption: make(chan time.Time)}
	controller := &checkpointIntentController{captured: make(chan AgentCheckpointCaptureRequest, 1)}
	intents := startAgentCheckpointLiveIntents(context.Background(), controller, &process, AgentCheckpointCaptureRequest{}, time.Millisecond, nil, nil)
	if intents == nil {
		t.Fatal("did not start elapsed checkpoint intent watcher")
	}
	defer intents.Stop()

	select {
	case capture := <-controller.captured:
		if capture.Trigger != CheckpointCaptureTriggerElapsed || capture.Boundary != &process || capture.Quiescence != &process {
			t.Fatalf("elapsed capture = %#v", capture)
		}
	case <-time.After(time.Second):
		t.Fatal("elapsed intent did not reach live capture")
	}
}

func TestAgentCheckpointLiveIntentsScopesExplicitIntentToExactExecution(t *testing.T) {
	runID := snapshot.WorkflowRunID(19)
	identity := checkpoint.Identity{WorkflowRunID: &runID, BuildID: 42, PlanID: "plan-1", FunctionID: "review"}
	process := checkpointIntentSafeProcess{preemption: make(chan time.Time)}
	controller := &checkpointIntentController{captured: make(chan AgentCheckpointCaptureRequest, 1)}
	explicit := &checkpointIntentExplicitSource{identities: make(chan checkpoint.Identity, 1)}
	intents := startAgentCheckpointLiveIntents(context.Background(), controller, &process, AgentCheckpointCaptureRequest{Provenance: CheckpointCaptureProvenance{Identity: identity}}, time.Hour, explicit, nil)
	if intents == nil {
		t.Fatal("did not start explicit checkpoint intent watcher")
	}
	defer intents.Stop()

	select {
	case got := <-explicit.identities:
		if !sameAgentCheckpointIdentity(got, identity) {
			t.Fatalf("explicit intent identity = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit intent source was not scoped to its execution")
	}
	select {
	case capture := <-controller.captured:
		if capture.Trigger != CheckpointCaptureTriggerExplicit || capture.Boundary != &process || capture.Quiescence != &process {
			t.Fatalf("trigger = %q", capture.Trigger)
		}
	case <-time.After(time.Second):
		t.Fatal("explicit intent did not reach live capture")
	}
}

type checkpointIntentController struct {
	mu               sync.Mutex
	liveCaptureCount int
	captured         chan AgentCheckpointCaptureRequest
}

func (controller *checkpointIntentController) Prepare(context.Context) (checkpoint.Attempt, error) {
	return checkpoint.Attempt{}, nil
}

func (controller *checkpointIntentController) MarkRunning(context.Context) (checkpoint.Attempt, error) {
	return checkpoint.Attempt{}, nil
}

func (controller *checkpointIntentController) MarkInterrupted(context.Context, checkpoint.Attempt, runtime.InterruptionReason) (checkpoint.Attempt, error) {
	return checkpoint.Attempt{}, nil
}

func (controller *checkpointIntentController) CaptureLive(_ context.Context, trigger CheckpointCaptureTrigger, request AgentCheckpointCaptureRequest) (AgentCheckpointCaptureResult, error) {
	controller.mu.Lock()
	controller.liveCaptureCount++
	controller.mu.Unlock()
	request.Trigger = trigger
	if controller.captured != nil {
		controller.captured <- request
	}
	return AgentCheckpointCaptureResult{Status: CheckpointCaptureSkipped}, nil
}

func (controller *checkpointIntentController) FinalizeSucceeded(context.Context, AgentCheckpointCaptureRequest) (AgentCheckpointFinalizeResult, error) {
	return AgentCheckpointFinalizeResult{}, nil
}

type checkpointIntentProcess struct{}

func (checkpointIntentProcess) ID() string { return agentProcessID }

func (checkpointIntentProcess) Wait(context.Context) (runtime.ProcessResult, error) {
	return runtime.ProcessResult{}, nil
}

func (checkpointIntentProcess) SetTTY(runtime.TTYSpec) error { return nil }

type checkpointIntentSafeProcess struct {
	checkpointIntentProcess
	preemption <-chan time.Time
}

func (process *checkpointIntentSafeProcess) AcquireSafeBoundary(context.Context) (runtime.SafeBoundaryLease, error) {
	return checkpointIntentSafeBoundary{}, nil
}

func (process *checkpointIntentSafeProcess) AcquireCheckpointCapture(context.Context, int64) (runtime.CheckpointCaptureLease, error) {
	return checkpointIntentCaptureLease{}, nil
}

func (process *checkpointIntentSafeProcess) WaitForCheckpointPreemption(ctx context.Context) (time.Time, error) {
	select {
	case at := <-process.preemption:
		return at, nil
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
}

type checkpointIntentSafeBoundary struct{}

func (checkpointIntentSafeBoundary) Release(context.Context) error { return nil }

type checkpointIntentCaptureLease struct{}

func (checkpointIntentCaptureLease) CaptureTarget() runtime.CheckpointCaptureTarget {
	return runtime.CheckpointCaptureTarget{}
}

func (checkpointIntentCaptureLease) Release(context.Context) error { return nil }

type checkpointIntentExplicitSource struct {
	once       sync.Once
	identities chan checkpoint.Identity
}

func (source *checkpointIntentExplicitSource) WaitForAgentCheckpointIntent(ctx context.Context, identity checkpoint.Identity) error {
	called := false
	source.once.Do(func() {
		called = true
		source.identities <- identity
	})
	if called {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}
