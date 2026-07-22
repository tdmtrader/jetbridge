package provenance

import (
	"encoding/json"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

func TestFacadeExposesNeutralCaptureAPI(t *testing.T) {
	config, err := atc.CanonicalJSON(atc.Config{Jobs: atc.JobConfigs{{
		Name: "run", PlanSequence: []atc.Step{{Config: &atc.AgentStep{Name: "agent"}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	captured, err := FromPlan(Input{
		WorkflowRunID: snapshot.WorkflowRunID(1), WorkflowDefinitionID: 1, ConcreteConfig: json.RawMessage(config),
	}, atc.Plan{ID: "agent", Agent: &atc.AgentPlan{Name: "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.CanonicalPlan) == 0 || captured.PlanHash == "" || len(captured.ResolvedDependencies) == 0 {
		t.Fatalf("capture = %#v", captured)
	}
	verified, err := VerifyFrozen(
		Input{WorkflowRunID: 1, WorkflowDefinitionID: 1, ConcreteConfig: json.RawMessage(config)},
		captured.CanonicalPlan, captured.PlanHash, captured.ResolvedDependencies,
	)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PlanHash != captured.PlanHash {
		t.Fatalf("verified hash = %q, want %q", verified.PlanHash, captured.PlanHash)
	}
}
