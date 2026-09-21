package engine

import (
	"context"
	"testing"

	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runs"
)

// The fallback admitter's error is a fault, and has to stay one.
//
// The distinction is invisible at the call site: exec.RunPipelineStep asks
// runs.IsRefusal, and a refusal prints on the build's stderr and fails the
// step while a fault travels the engine's error path. Which one this error is
// depends on nothing but the value's type, so the classification can be
// reversed by a well-meaning edit -- wrapping it in one of the port's
// sentinels, or giving it an AdmissionRefusal method -- with nothing failing.
// This is the thing that fails.
//
// A missing port is a deployment defect. The pipeline's config is fine, the
// same plan admits a run the moment the web node is wired, and the person who
// has to know is the operator rather than whoever pushed the pipeline. A
// refusal would put the message in a build log that is world-readable on a
// public pipeline and would take the step past LogError and RetryError, which
// are exactly the two things that should see it.
func TestUnwiredChildRunAdmitterErrorIsAFault(t *testing.T) {
	_, err := unwiredChildRunAdmitter{}.AdmitChildRun(context.Background(), exec.ChildRunRequest{})
	if err == nil {
		t.Fatal("expected the unwired admitter to refuse every admission, got no error")
	}

	if runs.IsRefusal(err) {
		t.Errorf("runs.IsRefusal classified the unwired admitter's error as a refusal: %v\n"+
			"a missing port is a deployment defect, not a fact about the pipeline's config; "+
			"it must travel the engine's error path", err)
	}
}

// A factory built without an admitter hands the step the fallback rather than
// a nil interface, which is the whole reason the fallback is a value: a nil
// one would turn the first run_pipeline plan a test harness or the brine
// runner ever carries into a nil dereference inside the step.
//
// The other half -- that a supplied admitter is handed on unchanged -- is not
// asserted here, because saying it would need a second implementation of the
// port written for the purpose. atc/integration/run_pipeline_step_test.go says
// it against the real one: a real build admits a real run through the adapter
// the composition root supplies.
func TestAdmitterOrFallbackWithoutAnAdmitter(t *testing.T) {
	unwired := (&coreStepFactory{}).admitterOrFallback()
	if unwired == nil {
		t.Fatal("a factory built without an admitter handed the step a nil interface")
	}
	if _, isFallback := unwired.(unwiredChildRunAdmitter); !isFallback {
		t.Errorf("expected the unwired fallback, got %T", unwired)
	}
}
