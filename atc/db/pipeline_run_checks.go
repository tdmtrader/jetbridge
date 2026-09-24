package db

import (
	"context"

	"github.com/concourse/concourse/atc"
)

// A Run cannot own an execution that disappears with an ATC process. Its
// resource checks use ordinary durable builds and the same admission fence.
func durableRunChecks(_ context.Context, _ DbConn, checkable PipelineRef) (bool, error) {
	_, found := checkable.PipelineRunID()
	return found, nil
}

func admitRunCheck(tx Tx, pipelineID int) (int, error) {
	run, found, err := lockPipelineRunForPayload(tx, pipelineID, 0)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	if run.CancellationRequested() {
		return 0, ErrPipelineRunCancelling
	}
	if run.Status() != atc.RunStatusRunning {
		return 0, ErrPipelineRunNotRunning
	}
	return run.ID(), nil
}
