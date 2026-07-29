package exec

import (
	"context"
	"testing"

	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/runtime/runtimetest"
)

func TestAgentDurableRunningReentryNeverFallsBackToASecondProcess(t *testing.T) {
	spec := runtime.ProcessSpec{ID: agentProcessID, Path: "agent-runner", Dir: "/work"}
	container := runtimetest.NewContainer().WithProcess(spec, runtimetest.ProcessStub{})

	if _, err := attachOrRunAgentAttempt(context.Background(), container, spec, runtime.ProcessIO{}, true); err == nil {
		t.Fatal("durable running reentry unexpectedly launched after attach failed")
	}
	if got := len(container.RunningProcesses()); got != 0 {
		t.Fatalf("durable running reentry launched %d processes", got)
	}
}

func TestAgentMaterializingAttemptMayLaunchAfterAttachMiss(t *testing.T) {
	spec := runtime.ProcessSpec{ID: agentProcessID, Path: "agent-runner", Dir: "/work"}
	container := runtimetest.NewContainer().WithProcess(spec, runtimetest.ProcessStub{})

	if _, err := attachOrRunAgentAttempt(context.Background(), container, spec, runtime.ProcessIO{}, false); err != nil {
		t.Fatal(err)
	}
	if got := len(container.RunningProcesses()); got != 1 {
		t.Fatalf("materializing attempt launched %d processes, want 1", got)
	}
}
