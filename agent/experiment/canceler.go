package experiment

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/snapshot"
)

type CancellationWork struct {
	ExperimentID   ID
	TeamID         int
	WorkflowRunIDs []snapshot.WorkflowRunID
}

func (work CancellationWork) Validate() error {
	if err := work.ExperimentID.Validate(); err != nil {
		return err
	}
	if work.TeamID <= 0 {
		return fmt.Errorf("experiment cancellation: team ID must be positive")
	}
	seen := make(map[snapshot.WorkflowRunID]struct{}, len(work.WorkflowRunIDs))
	for _, id := range work.WorkflowRunIDs {
		if err := id.Validate(); err != nil {
			return err
		}
		if _, found := seen[id]; found {
			return fmt.Errorf("experiment cancellation: duplicate workflow run %s", id.String())
		}
		seen[id] = struct{}{}
	}
	return nil
}

type CancellationStore interface {
	ClaimCancellations(context.Context, int) ([]CancellationWork, error)
	FinalizeCancellation(context.Context, int, ID) (bool, error)
}

type RunCanceler interface {
	CancelRun(context.Context, int, snapshot.WorkflowRunID) error
}

type CancellationReconciler struct {
	store    CancellationStore
	canceler RunCanceler
	limit    int
}

func NewCancellationReconciler(store CancellationStore, canceler RunCanceler, limit int) (*CancellationReconciler, error) {
	if nilInterface(store) || nilInterface(canceler) || limit <= 0 {
		return nil, fmt.Errorf("experiment cancellation: store, run canceler, and positive limit are required")
	}
	return &CancellationReconciler{store: store, canceler: canceler, limit: limit}, nil
}

func (reconciler *CancellationReconciler) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("experiment cancellation: context is required")
	}
	work, err := reconciler.store.ClaimCancellations(ctx, reconciler.limit)
	if err != nil {
		return fmt.Errorf("experiment cancellation: claim: %w", err)
	}
	var failures []error
	for index, item := range work {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := item.Validate(); err != nil {
			failures = append(failures, fmt.Errorf("experiment cancellation: claimed item %d: %w", index, err))
			continue
		}
		failed := false
		for _, runID := range item.WorkflowRunIDs {
			if err := reconciler.canceler.CancelRun(ctx, item.TeamID, runID); err != nil {
				failed = true
				failures = append(failures, fmt.Errorf(
					"experiment cancellation: experiment %s workflow run %s: %w",
					item.ExperimentID.String(), runID.String(), err,
				))
			}
		}
		if failed {
			continue
		}
		if _, err := reconciler.store.FinalizeCancellation(ctx, item.TeamID, item.ExperimentID); err != nil {
			failures = append(failures, fmt.Errorf("experiment cancellation: finalize %s: %w", item.ExperimentID.String(), err))
		}
	}
	return errors.Join(failures...)
}
