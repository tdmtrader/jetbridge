package gc

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/hashicorp/go-multierror"
)

// DefaultPipelineRunReclaimBatchSize bounds how many runs one pass reclaims.
// It is the default for --pipeline-run-reclaim-batch, not a constant: a
// cluster whose backlog metric shows the reclaimer falling behind its one
// minute interval needs a bigger batch, and one whose reclaim duration is
// long needs a smaller one.
const DefaultPipelineRunReclaimBatchSize = 20

type pipelineRunReclaimer struct {
	lifecycle db.PipelineRunReclaimLifecycle
	now       func() time.Time
	batchSize int
}

func NewPipelineRunReclaimer(lifecycle db.PipelineRunReclaimLifecycle, now func() time.Time, batchSize int) *pipelineRunReclaimer {
	if batchSize <= 0 {
		batchSize = DefaultPipelineRunReclaimBatchSize
	}
	return &pipelineRunReclaimer{lifecycle: lifecycle, now: now, batchSize: batchSize}
}

// BatchSize is the bound this reclaimer will ask its lifecycle for. It is
// exported because the value comes from an operator flag several hops away,
// and a flag that never reaches its component is indistinguishable from a
// flag that works until someone reads the batch the component actually got.
func (r *pipelineRunReclaimer) BatchSize() int {
	return r.batchSize
}

func (r *pipelineRunReclaimer) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("pipeline-run-reclaimer")

	// Deferred, and armed before the first query, so that a pass returning
	// early on a failed query still reports the time it burned -- which is
	// exactly the pass whose cost is otherwise invisible.
	start := r.now()
	defer func() {
		metric.PipelineRunReclaimDuration{Duration: r.now().Sub(start)}.Emit(logger)
	}()

	// Measured before the batch and unbounded by it: the pass's own candidate
	// list is capped at batchSize, so it could never show the reclaimer
	// falling behind. Emitted on every pass, empty ones included, so the
	// series returns to zero instead of going stale at its last non-zero
	// value.
	backlog, err := r.lifecycle.ReclaimBacklog()
	if err != nil {
		return err
	}
	metric.PipelineRunReclaimBacklog{Runs: backlog}.Emit(logger)

	ids, err := r.lifecycle.ReclaimCandidateRunIDs(r.batchSize)
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
