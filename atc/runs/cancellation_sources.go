package runs

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// CancellationSource reaches the original node without depending on Kubernetes
// in the Run service. Neither a missing Pod nor a replacement node is closure.
type CancellationSource interface {
	InterruptExecution(context.Context, string, executioncontrol.Acknowledgement) error
	RecoverExecutionOutcome(context.Context, string, executioncontrol.Acknowledgement) (executioncontrol.ClassifyResult, error)
	ClassifyExecution(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.ClassifyResult, error)
	StopExecution(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error)
}

// CancellationActionSet composes owners without silently completing a kind that
// none implements. Each handler must refuse unsupported kinds before any work.
type CancellationActionSet []CancellationActions

type CancellationActionFunc func(context.Context, db.RunCancellationLease, db.RunCancellationOperation) (db.RunCancellationDebt, error)

func (action CancellationActionFunc) ExecuteCancellationOperation(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
	return action(ctx, lease, op)
}

func (actions CancellationActionSet) ExecuteCancellationOperation(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
	for _, action := range actions {
		debt, err := action.ExecuteCancellationOperation(ctx, lease, op)
		if !errors.Is(err, db.ErrRunCancellationExternalWork) {
			return debt, err
		}
	}
	return db.CancellationUnavailable, db.ErrRunCancellationExternalWork
}

func cancellationSourceDebt(err error) db.RunCancellationDebt {
	switch {
	case err == nil:
		return db.CancellationDone
	case errors.Is(err, atc.ErrRunOutputPending):
		return db.CancellationPending
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return db.CancellationTimeout
	case errors.Is(err, db.ErrRunCancellationProgressStale), errors.Is(err, db.ErrRunCancellationLeaseLost), errors.Is(err, output.ErrConflict), errors.Is(err, output.ErrInvalidIdentity):
		return db.CancellationConflict
	default:
		return db.CancellationUnavailable
	}
}
