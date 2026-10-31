package gc

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
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

	// Deferred, and armed before the first query, so that a pass returning
	// early on a failed query still reports the time it burned -- which is
	// exactly the pass whose cost is otherwise invisible.
	start := r.now()
	defer func() {
		metric.PipelineRunReclaimDuration(r.now().Sub(start))
	}()
	if err := r.lifecycle.ReleaseExpiredInputUploads(ctx, pipelineRunReclaimBatchSize); err != nil {
		return err
	}

	// Measured before the batch and unbounded by it: the pass's own candidate
	// list is capped at batchSize, so it could never show the reclaimer
	// falling behind. Emitted on every pass, empty ones included, so the
	// series returns to zero instead of going stale at its last non-zero
	// value.
	backlog, err := r.lifecycle.ReclaimBacklog()
	if err != nil {
		return err
	}
	metric.PipelineRunReclaimBacklog(backlog)

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
