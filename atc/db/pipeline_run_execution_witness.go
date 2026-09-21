package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type RunExecutionVerifier interface {
	VerifyExecution(executioncontrol.Acknowledgement) error
}

func retainedRunExecutionStart(ctx context.Context, tx Tx, identity executioncontrol.Identity) (*executioncontrol.Acknowledgement, error) {
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT witness FROM pipeline_run_execution_starts WHERE execution_id=$1 AND execution_fence=$2`, string(identity.ExecutionID), int64(identity.Fence)).Scan(&body)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var witness executioncontrol.Acknowledgement
	if err := json.Unmarshal(body, &witness); err != nil {
		return nil, err
	}
	if err := witness.Validate(); err != nil {
		return nil, err
	}
	if witness.Identity != identity || witness.Kind != executioncontrol.AcknowledgementStart {
		return nil, output.ErrInvalidIdentity
	}
	return &witness, nil
}

// RecordRunExecutionWitness retains a node fact, never new start authority.
// A cancellation can race the network reply; the fact remains recordable under
// the Run boundary so recovery can reconcile that exact execution.
func (f *pipelineRunFactory) RecordRunExecutionWitness(ctx context.Context, tx Tx, buildID int, planID atc.PlanID, witness executioncontrol.Acknowledgement, verifier RunExecutionVerifier) error {
	a, found, err := f.RunExecution(ctx, tx, buildID, planID)
	if err != nil {
		return err
	}
	if !found {
		return output.ErrInvalidIdentity
	}
	if _, err = lockRunResultPublication(ctx, tx, a.RunID); err != nil {
		return err
	}
	if verifier == nil {
		return fmt.Errorf("%w: no Run execution verifier", output.ErrIncomplete)
	}
	if err = witness.Validate(); err != nil {
		return err
	}
	if err = verifier.VerifyExecution(witness); err != nil {
		return err
	}
	if witness.Identity != a.Identity || witness.ActivationEpoch != executioncontrol.ActivationEpoch(a.Epoch) || string(witness.NodeUID) != a.NodeUID || witness.PodUID == "" {
		return fmt.Errorf("%w: witness does not match the admitted Run execution", output.ErrInvalidIdentity)
	}
	body, err := json.Marshal(witness)
	if err != nil {
		return err
	}
	if witness.Kind == executioncontrol.AcknowledgementStart {
		if _, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_execution_starts(execution_id,execution_fence,witness)
 VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), body); err != nil {
			return err
		}
		var matches bool
		if err = tx.QueryRowContext(ctx, `SELECT witness=$3::jsonb FROM pipeline_run_execution_starts WHERE execution_id=$1 AND execution_fence=$2`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), body).Scan(&matches); err != nil {
			return err
		}
		if !matches {
			return fmt.Errorf("%w: exact start witness changed", output.ErrConflict)
		}
		return nil
	}
	if witness.Kind != executioncontrol.AcknowledgementFinish && witness.Kind != executioncontrol.AcknowledgementStop {
		return output.ErrInvalidIdentity
	}
	var startBody []byte
	if err = tx.QueryRowContext(ctx, `SELECT witness FROM pipeline_run_execution_starts WHERE execution_id=$1 AND execution_fence=$2`, string(a.Identity.ExecutionID), int64(a.Identity.Fence)).Scan(&startBody); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("%w: no retained exact start", output.ErrIncomplete)
		}
		return err
	}
	var start executioncontrol.Acknowledgement
	if err = json.Unmarshal(startBody, &start); err != nil {
		return err
	}
	if err = verifier.VerifyExecution(start); err != nil {
		return err
	}
	if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != witness.Identity || start.PodUID != witness.PodUID || start.ProcessIdentity != witness.ProcessIdentity || start.NodeUID != witness.NodeUID || start.ActivationEpoch != witness.ActivationEpoch || start.LedgerSequence >= witness.LedgerSequence {
		return fmt.Errorf("%w: outcome does not close the retained exact start", output.ErrInvalidIdentity)
	}
	observation := RunOutputCancellationEvidence{NodeUID: a.NodeUID, Execution: executioncontrol.ClassifyResult{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: a.Identity, Classification: witness.Kind.Classification(), Acknowledgement: &witness}}
	body, err = json.Marshal(observation)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_execution_closures(execution_id,execution_fence,classification,observation)
 VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), string(observation.Execution.Classification), body); err != nil {
		return err
	}
	var matches bool
	if err = tx.QueryRowContext(ctx, `SELECT observation=$3::jsonb FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2`, string(a.Identity.ExecutionID), int64(a.Identity.Fence), body).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("%w: exact closure witness changed", output.ErrConflict)
	}
	return nil
}
