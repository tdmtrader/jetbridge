package db

import (
	"context"

	"github.com/concourse/concourse/atc"
)

// A v2 Run cannot own an execution that disappears with an ATC process. Its
// resource checks use ordinary durable builds and the same admission fence.
func durableRunChecks(ctx context.Context, conn DbConn, checkable PipelineRef) (bool, error) {
	runID, found := checkable.PipelineRunID()
	if !found {
		return false, nil
	}
	var durable bool
	err := conn.QueryRowContext(ctx, `SELECT run_contract_version='v2' FROM pipeline_runs WHERE id=$1`, runID).Scan(&durable)
	return durable, err
}

func admitRunCheck(tx Tx, pipelineID int) (int, error) {
	run, found, err := lockPipelineRunForPayload(tx, pipelineID, 0)
	if err != nil {
		return 0, err
	}
	if !found || run.ContractVersion() != atc.RunContractV2 {
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
