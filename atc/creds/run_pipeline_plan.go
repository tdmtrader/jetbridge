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

// excludeNonLocalReferences leaves every reference that is not a build-local
// var in place, so that only `((.:name))` is interpolated.
//
// A run's params are stored on the run header, returned by the runs API to
// anyone who can view the team, and shown to the policy agent. Interpolating
// `((vault/x))` here would persist the secret in all three places for as long
// as the run exists, which is not something a pipeline author asked for by
// writing a reference. So the reference travels verbatim: materialisation
// substitutes the param value into the payload config (atc.RunConfig), and the
// payload resolves it at build time through the credential manager exactly the
// way a non-template pipeline resolves the same reference. Nothing is lost and
// nothing is stored.
//
// Build-local vars are the exception because they are the one kind that cannot
// survive the trip. `((.:name))` exists only inside the calling build -- it is
// a `load_var` result or an `across` value, held in that build's run state --
// so a payload that reached it verbatim would find no such var at all. Those
// are resolved here, and they are not secrets: they are values this build
// computed.
func excludeNonLocalReferences(reference vars.Reference) bool {
	return reference.Source != "."
}

func (s RunPipelinePlan) Evaluate() (atc.RunPipelinePlan, error) {
	var plan atc.RunPipelinePlan

	// Only the params are interpolated. The name of the template being called
	// is not, for the same reason set_pipeline's name is not (#5277): it
	// identifies the call, and a call whose identity varies with credentials
	// cannot be reasoned about.
	name := s.rawPlan.Name

	err := evaluateWithReferenceExclusion(s.variablesResolver, s.rawPlan, &plan, excludeNonLocalReferences)
	if err != nil {
		return atc.RunPipelinePlan{}, err
	}
	plan.Name = name

	return plan, nil
}
