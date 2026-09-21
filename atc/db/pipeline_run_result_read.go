package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RunResultRead is a retained binding, not a caller-supplied tree or claim.
type RunResultRead struct {
	Binding atc.RunResultBinding
	Epoch   executioncontrol.ActivationEpoch
}

func LoadRunResultRead(ctx context.Context, conn DbConn, runID int, name string) (RunResultRead, error) {
	var result RunResultRead
	var body []byte
	var status atc.RunStatus
	err := conn.QueryRowContext(ctx, `SELECT status, result_manifest->$2, activation_epoch FROM pipeline_runs WHERE id=$1 AND run_contract_version='v2'`, runID, name).Scan(&status, &body, &result.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return result, output.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if status == atc.RunStatusRunning {
		return result, atc.ErrRunResultPending
	}
	if status != atc.RunStatusSucceeded || len(body) == 0 {
		return result, output.ErrNotFound
	}
	if err = json.Unmarshal(body, &result.Binding); err != nil {
		return result, err
	}
	return result, nil
}

// LockRunResultRead takes the same domain prefix as terminal publication and
// revalidates the binding before a caller enters the Hangar lease suffix.
// Retained headers need no disposable payload or base-template lock.
func LockRunResultRead(ctx context.Context, tx Tx, runID int, name string, expected RunResultRead) error {
	run, err := lockRunResultPublication(ctx, tx, runID)
	if err != nil {
		return err
	}
	if run.Status() != atc.RunStatusSucceeded || run.ContractVersion() != atc.RunContractV2 || run.ActivationEpoch() != int64(expected.Epoch) {
		return atc.ErrRunResultsUnavailable
	}
	var body []byte
	if err = tx.QueryRowContext(ctx, `SELECT result_manifest->$2 FROM pipeline_runs WHERE id=$1`, runID, name).Scan(&body); err != nil {
		return err
	}
	var binding atc.RunResultBinding
	if json.Unmarshal(body, &binding) != nil || binding != expected.Binding {
		return output.ErrConflict
	}
	return nil
}
