package atc_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestValidateUntrustedPlanRejectsBrokerAuthorityAtEveryPlanLevel(t *testing.T) {
	tests := []struct {
		name string
		plan atc.Plan
		want string
	}{
		{
			name: "direct agent authority",
			plan: atc.Plan{Agent: &atc.AgentPlan{
				BrokerAuthority: []atc.AgentBrokerProfile{{FunctionID: "agent"}},
			}},
			want: "broker_authority is reserved for server-rendered workflow runs",
		},
		{
			name: "nested agent broker marker",
			plan: atc.Plan{Do: &atc.DoPlan{{Agent: &atc.AgentPlan{
				Env: map[string]string{"CONCOURSE_AGENT_BROKER_MCP": "1"},
			}}}},
			want: "CONCOURSE_AGENT_BROKER_MCP is reserved for the managed broker",
		},
		{
			name: "across template agent authority",
			plan: atc.Plan{Across: &atc.AcrossPlan{
				Vars:            []atc.AcrossVar{{Var: "item", Values: []string{"one"}}},
				SubStepTemplate: `{"id":"agent","agent":{"broker_authority":[{"function_id":"agent"}]}}`,
			}},
			want: "broker_authority is reserved for server-rendered workflow runs",
		},
		{
			name: "YAML across template broker marker",
			plan: atc.Plan{Across: &atc.AcrossPlan{
				Vars: []atc.AcrossVar{{Var: "item", Values: []string{"one"}}},
				SubStepTemplate: `id: agent
agent:
  env:
    CONCOURSE_AGENT_BROKER_MCP: "1"`,
			}},
			want: "CONCOURSE_AGENT_BROKER_MCP is reserved for the managed broker",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := atc.ValidateUntrustedPlan(test.plan)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateUntrustedPlan() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateUntrustedPlanAllowsOrdinaryAgentPlans(t *testing.T) {
	err := atc.ValidateUntrustedPlan(atc.Plan{Agent: &atc.AgentPlan{
		Name: "agent",
		Env:  map[string]string{"SAFE": "value"},
	}})
	if err != nil {
		t.Fatalf("ValidateUntrustedPlan() error = %v, want nil", err)
	}
}
