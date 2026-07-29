package jetbridge

import (
	"context"
	"fmt"
	"net/http"

	"github.com/concourse/concourse/agent/checkpoint"
)

// CheckpointRestoreClient sends one exact, generation-pinned restore request
// to the daemon endpoint selected for the already-scheduled node. It does not
// retry ambiguous materialization on another endpoint.
type CheckpointRestoreClient interface {
	RestoreCheckpoint(context.Context, string, checkpoint.RestoreRequest) (checkpoint.RestoreResult, error)
	VerifyCheckpointRestore(context.Context, string, checkpoint.RestoreRequest) (checkpoint.RestoreResult, error)
}

func (d *DaemonClient) VerifyCheckpointRestore(ctx context.Context, nodeName string, request checkpoint.RestoreRequest) (checkpoint.RestoreResult, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	endpoint, err := d.checkpointEndpoint(ctx, nodeName)
	if err != nil {
		return checkpoint.RestoreResult{}, err
	}
	var result checkpoint.RestoreResult
	if err := d.checkpointJSON(ctx, endpoint, "/checkpoints/v1/restore/verify", request, http.StatusOK, &result); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	if err := result.ValidateFor(request); err != nil {
		return checkpoint.RestoreResult{}, fmt.Errorf("checkpoint daemon restore verification response: %w", err)
	}
	return result, nil
}

var _ CheckpointRestoreClient = (*DaemonClient)(nil)

func (d *DaemonClient) RestoreCheckpoint(ctx context.Context, nodeName string, request checkpoint.RestoreRequest) (checkpoint.RestoreResult, error) {
	if err := request.Validate(); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	endpoint, err := d.checkpointEndpoint(ctx, nodeName)
	if err != nil {
		return checkpoint.RestoreResult{}, err
	}
	var result checkpoint.RestoreResult
	if err := d.checkpointJSON(ctx, endpoint, "/checkpoints/v1/restore", request, http.StatusOK, &result); err != nil {
		return checkpoint.RestoreResult{}, err
	}
	if err := result.ValidateFor(request); err != nil {
		return checkpoint.RestoreResult{}, fmt.Errorf("checkpoint daemon restore response: %w", err)
	}
	return result, nil
}
