package resourcecapture

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc/db"
)

const (
	FinalizerBatchSize = 100
	FinalizerActor     = "system:resource-capture"
)

var ErrFinalizerPass = errors.New("resource capture: background finalization pass failed")

// PendingOutputLister discovers succeeded server-owned capture generations
// whose exact available output does not yet carry the finalizer's durable pin.
// Implementations must derive every field from persisted server evidence.
type PendingOutputLister interface {
	ListPendingResourceCaptureOutputs(context.Context, string, int) ([]db.ResourceCaptureOutput, error)
}

// Finalizer turns the build-scoped retention created by snapshot sealing into
// a durable pin without depending on an API client to poll after completion.
type Finalizer struct {
	pending PendingOutputLister
	outputs OutputStore
}

func NewFinalizer(pending PendingOutputLister, outputs OutputStore) (*Finalizer, error) {
	if isNil(pending) || isNil(outputs) {
		return nil, fmt.Errorf("resource capture: pending output lister and output store are required")
	}
	return &Finalizer{pending: pending, outputs: outputs}, nil
}

func (finalizer *Finalizer) Run(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	requests, err := finalizer.pending.ListPendingResourceCaptureOutputs(ctx, FinalizerActor, FinalizerBatchSize)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return contextErr
		}
		return ErrFinalizerPass
	}
	failed := false
	for _, candidate := range requests {
		if err := contextError(ctx); err != nil {
			return err
		}
		request := OutputRequest{
			TeamID: candidate.TeamID, TeamName: candidate.TeamName, PipelineRunID: candidate.PipelineRunID,
			OperationKey: candidate.OperationKey, OutputPort: candidate.OutputPort,
			ExpectedType: candidate.ExpectedType, Actor: FinalizerActor,
		}
		manifest, pinned, err := finalizer.outputs.Finalize(ctx, request)
		if err != nil || !pinned || manifest.ID == 0 {
			failed = true
		}
	}
	if failed {
		return ErrFinalizerPass
	}
	return nil
}
