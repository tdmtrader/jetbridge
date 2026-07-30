package atc

import (
	"fmt"

	"github.com/concourse/concourse/vars"
	yamlv2 "go.yaml.in/yaml/v2"
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
	return validateUntrustedAcrossTemplate(plan.Across.SubStepTemplate)
}

func validateUntrustedAcrossTemplate(rawTemplate string) error {
	var genericTemplate any
	if err := yamlv2.Unmarshal([]byte(rawTemplate), &genericTemplate); err != nil {
		return fmt.Errorf("cannot decode across template: %w", err)
	}
	if err := rejectDynamicTemplateMapKeys(genericTemplate); err != nil {
		return err
	}

	var template Plan
	if err := yaml.Unmarshal([]byte(rawTemplate), &template); err != nil {
		return fmt.Errorf("cannot decode across template: %w", err)
	}
	return validateUntrustedPlan(template)
}

func rejectDynamicTemplateMapKeys(value any) error {
	switch typed := value.(type) {
	case map[any]any:
		for key, child := range typed {
			if keyString, ok := key.(string); ok && len(vars.ExtractVarRefs(keyString)) > 0 {
				return fmt.Errorf("across template has a dynamically interpolated mapping key")
			}
			if err := rejectDynamicTemplateMapKeys(child); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, child := range typed {
			if len(vars.ExtractVarRefs(key)) > 0 {
				return fmt.Errorf("across template has a dynamically interpolated mapping key")
			}
			if err := rejectDynamicTemplateMapKeys(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectDynamicTemplateMapKeys(child); err != nil {
				return err
			}
		}
	}
	return nil
}
