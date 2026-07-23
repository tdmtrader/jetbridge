package workflowrun

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
)

// ExperimentRunCancelerAdapter treats an already-terminal or already-collected
// durable run as canceled work and leaves active canceling runs for the next
// restart-safe experiment reconciliation pass.
type ExperimentRunCancelerAdapter struct {
	canceler *Canceler
}

func NewExperimentRunCancelerAdapter(canceler *Canceler) (*ExperimentRunCancelerAdapter, error) {
	if canceler == nil {
		return nil, fmt.Errorf("workflow run: experiment cancellation requires a canceler")
	}
	return &ExperimentRunCancelerAdapter{canceler: canceler}, nil
}

func (adapter *ExperimentRunCancelerAdapter) CancelRun(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) error {
	_, _, err := adapter.canceler.Cancel(ctx, teamID, id)
	if errors.Is(err, ErrCancelConflict) {
		return nil
	}
	return err
}

var _ experiment.RunCanceler = (*ExperimentRunCancelerAdapter)(nil)
