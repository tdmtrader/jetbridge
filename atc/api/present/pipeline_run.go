package present

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type PipelineRunOptions struct {
	AuthorizedForParams bool
	CanEnterPayload     bool
}

// PipelineRun presents a durable run with its separately loaded child snapshot.
// A nil child is the only reclaimed state; this presenter never loads or builds
// a child reference itself.
func PipelineRun(savedRun db.PipelineRun, instancePipeline db.Pipeline, options PipelineRunOptions) atc.PipelineRun {
	atcRun := atc.PipelineRun{
		ID:                 savedRun.ID(),
		TemplatePipelineID: savedRun.TemplatePipelineID(),
		Number:             savedRun.Number(),
		Status:             savedRun.Status(),
		CreatedBy:          savedRun.CreatedBy(),
		CreatedAt:          savedRun.CreatedAt(),
		CompletedAt:        savedRun.CompletedAt(),
		ReclaimRetryAfter:  savedRun.ReclaimRetryAfter(),
		Reclaimed:          instancePipeline == nil,
	}

	if options.AuthorizedForParams {
		params := savedRun.Params()
		if params == nil {
			params = atc.Params{}
		}
		configHash := savedRun.ConfigHash()
		atcRun.Params = &params
		atcRun.ConfigHash = &configHash
	}

	if instancePipeline != nil && options.CanEnterPayload {
		atcRun.InstanceRef = &atc.PipelineIdentifier{
			TeamName:     instancePipeline.TeamName(),
			PipelineName: instancePipeline.Name(),
			InstanceVars: instancePipeline.InstanceVars(),
		}
	}

	return atcRun
}
