package creds

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/vars"
)

type RunPipelinePlan struct {
	variablesResolver vars.Variables
	rawPlan           atc.RunPipelinePlan
}

func NewRunPipelinePlan(variables vars.Variables, plan atc.RunPipelinePlan) RunPipelinePlan {
	return RunPipelinePlan{
		variablesResolver: variables,
		rawPlan:           plan,
	}
}

func (s RunPipelinePlan) Evaluate() (atc.RunPipelinePlan, error) {
	var plan atc.RunPipelinePlan

	// Only the params are interpolated. The name of the template being called
	// is not, for the same reason set_pipeline's name is not (#5277): it
	// identifies the call, and a call whose identity varies with credentials
	// cannot be reasoned about.
	name := s.rawPlan.Name

	err := evaluate(s.variablesResolver, s.rawPlan, &plan)
	if err != nil {
		return atc.RunPipelinePlan{}, err
	}
	plan.Name = name

	return plan, nil
}
