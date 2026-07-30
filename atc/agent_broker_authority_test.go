package atc_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestStepValidatorRejectsAuthoredBrokerAuthority(t *testing.T) {
	validator := atc.NewStepValidator(atc.Config{}, []string{"jobs(test)", ".plan"})
	if err := validator.Validate(atc.Step{Config: &atc.AgentStep{
		Name: "agent", Prompt: "work", FunctionID: "agent",
		BrokerAuthority: []atc.AgentBrokerProfile{{FunctionID: "agent"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(validator.Errors, "\n"), "broker_authority is server-derived") {
		t.Fatalf("validator errors = %#v, want authored broker authority rejection", validator.Errors)
	}
}

func TestStepValidatorRejectsAuthoredBrokerMCPMarker(t *testing.T) {
	validator := atc.NewStepValidator(atc.Config{}, []string{"jobs(test)", ".plan"})
	if err := validator.Validate(atc.Step{Config: &atc.AgentStep{
		Name: "agent", Prompt: "work", Env: atc.TaskEnv{"CONCOURSE_AGENT_BROKER_MCP": "1"},
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(validator.Errors, "\n"), "CONCOURSE_AGENT_BROKER_MCP is reserved") {
		t.Fatalf("validator errors = %#v, want authored broker marker rejection", validator.Errors)
	}
}
