package engine

import (
	"code.cloudfoundry.org/clock"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
)

// NewRunPipelineStepDelegate builds the delegate the run_pipeline step
// reports through. It is a buildStepDelegate plus one policy check: the step's
// whole visibility is the line it writes to stdout, so unlike set_pipeline's
// there is no event of its own for this delegate to save, but like
// set_pipeline's it has to screen what the build is about to do against the
// policy agent that the API route would have screened.
func NewRunPipelineStepDelegate(
	build db.Build,
	planID atc.PlanID,
	state exec.RunState,
	clock clock.Clock,
	policyChecker policy.Checker,
) *runPipelineStepDelegate {
	return &runPipelineStepDelegate{
		buildStepDelegate{
			build:         build,
			planID:        planID,
			clock:         clock,
			state:         state,
			stdout:        nil,
			stderr:        nil,
			policyChecker: policyChecker,
		},
	}
}

type runPipelineStepDelegate struct {
	buildStepDelegate
}

// CheckRunPipelinePolicy screens the admission the step is about to make.
//
// Team and Pipeline on the input are the *calling* build's, which is what
// set_pipeline's check reports and what a policy written against either one
// will expect: they say who is doing this. What is being done -- the target
// team, the template, the params -- is the data.
//
// The params are what the run will record, which is credential *references*
// (`((vault/x))`) rather than the secrets they name; only the calling build's
// own local vars are resolved by the time they get here. An agent that must
// judge a run by a secret's value cannot, by construction, and that is the
// same deal set_pipeline's check offers for the config it screens.
func (delegate *runPipelineStepDelegate) CheckRunPipelinePolicy(team string, pipeline string, params atc.RunParams) error {
	if !delegate.policyChecker.ShouldCheckAction(policy.ActionRunPipeline) {
		return nil
	}

	return delegate.checkPolicy(policy.PolicyCheckInput{
		Action:   policy.ActionRunPipeline,
		Team:     delegate.build.TeamName(),
		Pipeline: delegate.build.PipelineName(),
		Data: exec.RunPipelinePolicyData{
			Team:     team,
			Pipeline: pipeline,
			Params:   params,
		},
	})
}
