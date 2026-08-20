package gc

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/hashicorp/go-multierror"
)

const pipelineRunReclaimBatchSize = 20

type pipelineRunReclaimer struct {
	lifecycle db.PipelineRunReclaimLifecycle
	now       func() time.Time
}

func NewPipelineRunReclaimer(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time) *pipelineRunReclaimer {
	return &pipelineRunReclaimer{lifecycle: lifecycle, now: now}
}

func (r *pipelineRunReclaimer) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("pipeline-run-reclaimer")
	ids, err := r.lifecycle.ReclaimCandidateRunIDs(pipelineRunReclaimBatchSize)
	if err != nil {
		return err
	}

	var errs error
	for _, id := range ids {
		_, err = r.lifecycle.DestroyReclaimableRun(id)
		if err == nil {
			continue
		}
		logger.Error("failed-to-reclaim-pipeline-run", err)
		errs = multierror.Append(errs, err)
		if deferErr := r.lifecycle.DeferRunReclaim(id, r.now().Add(5*time.Minute)); deferErr != nil {
			logger.Error("failed-to-defer-pipeline-run-reclaim", deferErr)
			errs = multierror.Append(errs, deferErr)
		}
	}
	return errs
}
