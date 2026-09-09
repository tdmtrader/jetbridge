package engine

import (
	"code.cloudfoundry.org/clock"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/policy"
)

// NewRunPipelineStepDelegate builds the delegate the run_pipeline step
// reports through. It is a buildStepDelegate and nothing more: the step's
// whole visibility is the line it writes to stdout, so unlike set_pipeline's
// there is no event of its own for this delegate to save.
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
