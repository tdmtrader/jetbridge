package atc

import (
	"fmt"

	"sigs.k8s.io/yaml"
)

const managedBrokerMCPEnv = "CONCOURSE_AGENT_BROKER_MCP"

// ValidateUntrustedPlan rejects broker fields from plan inputs which have not
// been rendered by a workflow run. Broker authority is execution-only data: a
// one-off plan must not be able to name a frozen profile or enable the managed
// broker endpoint. The walk includes across templates because those plans are
// decoded and executed after their variables are interpolated.
func ValidateUntrustedPlan(plan Plan) error {
	return validateUntrustedPlan(plan)
}

func validateUntrustedPlan(plan Plan) error {
	var validationErr error
	plan.Each(func(nested *Plan) {
		if validationErr != nil {
			return
		}
		validationErr = validateUntrustedPlanNode(*nested)
	})
	return validationErr
}

func validateUntrustedPlanNode(plan Plan) error {
	if plan.Agent != nil {
		if len(plan.Agent.BrokerAuthority) > 0 {
			return fmt.Errorf("agent broker_authority is reserved for server-rendered workflow runs")
		}
		if _, found := plan.Agent.Env[managedBrokerMCPEnv]; found {
			return fmt.Errorf("agent %s is reserved for the managed broker", managedBrokerMCPEnv)
		}
	}

	if plan.Across == nil {
		return nil
	}
	var template Plan
	if err := yaml.Unmarshal([]byte(plan.Across.SubStepTemplate), &template); err != nil {
		return nil
	}
	return validateUntrustedPlan(template)
}
