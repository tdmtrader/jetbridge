package present

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type PipelineRunOptions struct {
	AuthorizedForCancellation bool
	CanCancel                 bool
	AuthorizedForParams       bool
	CanEnterPayload           bool
}

// PipelineRun presents a durable run with its separately loaded child snapshot.
// A nil child is the only reclaimed state; this presenter never loads or builds
// a child reference itself.
func PipelineRun(savedRun db.PipelineRun, payload db.Pipeline, options PipelineRunOptions) atc.PipelineRun {
	atcRun := atc.PipelineRun{
		ContractVersion:    savedRun.ContractVersion(),
		ActivationEpoch:    savedRun.ActivationEpoch(),
		ID:                 savedRun.ID(),
		TemplatePipelineID: savedRun.TemplatePipelineID(),
		Number:             savedRun.Number(),
		Status:             savedRun.Status(),
		CreatedBy:          savedRun.CreatedBy(),
		CreatedAt:          savedRun.CreatedAt(),
		CompletedAt:        savedRun.CompletedAt(),
		Reclaimed:          payload == nil,
	}

	if options.AuthorizedForCancellation {
		atcRun.Cancellation = savedRun.CancellationRequest()
		atcRun.CanCancel = options.CanCancel && savedRun.Status() == atc.RunStatusRunning && !savedRun.CancellationRequested()
	}
	if options.AuthorizedForParams {
		params := savedRun.Params()
		if params == nil {
			params = atc.Params{}
		}
		configHash := savedRun.ConfigHash()
		atcRun.Params = &params
		atcRun.ConfigHash = &configHash

		// The reclaimer's backoff deadline is internal GC scheduling state.
		// It was previously set on every response, so an unauthenticated
		// viewer of an exposed template could read the ATC's GC timing for
		// every run of that template.
		atcRun.ReclaimRetryAfter = savedRun.ReclaimRetryAfter()

		// Caller intent is team history: redacted like params outside it.
		atcRun.CausedByRun = savedRun.CausedByRun()
		atcRun.Correlation = savedRun.Correlation()
	}

	if payload != nil && options.CanEnterPayload {
		atcRun.InstanceRef = &atc.PipelineIdentifier{
			TeamName:     payload.TeamName(),
			PipelineName: payload.Name(),
			InstanceVars: payload.InstanceVars(),
		}
	}

	return atcRun
}
