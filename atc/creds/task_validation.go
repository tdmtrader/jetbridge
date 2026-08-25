package creds

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/vars"
)

type TaskEnvValidator struct {
	variablesResolver vars.Variables
	rawTaskEnv        atc.TaskEnv
}

func NewTaskEnvValidator(variables vars.Variables, params atc.TaskEnv) TaskEnvValidator {
	return TaskEnvValidator{
		variablesResolver: variables,
		rawTaskEnv:        params,
	}
}

func (s TaskEnvValidator) Validate() error {
	return s.ValidateWithReferenceExclusion(nil)
}

// ValidateWithReferenceExclusion validates while leaving every reference
// matched by excludeReference in place.
func (s TaskEnvValidator) ValidateWithReferenceExclusion(excludeReference vars.ReferenceExclusion) error {
	var params atc.TaskEnv
	return evaluateWithReferenceExclusion(s.variablesResolver, s.rawTaskEnv, &params, excludeReference)
}

type TaskVarsValidator struct {
	variablesResolver vars.Variables
	rawTaskVars       atc.Params
}

func NewTaskVarsValidator(variables vars.Variables, taskVars atc.Params) TaskVarsValidator {
	return TaskVarsValidator{
		variablesResolver: variables,
		rawTaskVars:       taskVars,
	}
}

func (s TaskVarsValidator) Validate() error {
	return s.ValidateWithReferenceExclusion(nil)
}

// ValidateWithReferenceExclusion validates while leaving every reference
// matched by excludeReference in place.
func (s TaskVarsValidator) ValidateWithReferenceExclusion(excludeReference vars.ReferenceExclusion) error {
	var params atc.Params
	return evaluateWithReferenceExclusion(s.variablesResolver, s.rawTaskVars, &params, excludeReference)
}
