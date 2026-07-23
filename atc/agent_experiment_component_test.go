package atc_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestAgentExperimentComponentNamesAreStable(t *testing.T) {
	if atc.ComponentAgentExperimentRunner != "agent_experiment_runner" {
		t.Fatalf("runner component name = %q", atc.ComponentAgentExperimentRunner)
	}
	if atc.ComponentAgentExperimentEvaluator != "agent_experiment_evaluator" {
		t.Fatalf("evaluator component name = %q", atc.ComponentAgentExperimentEvaluator)
	}
	if atc.ComponentAgentExperimentCancellation != "agent_experiment_cancellation_reconciler" {
		t.Fatalf("cancellation component name = %q", atc.ComponentAgentExperimentCancellation)
	}
}
