package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

var ErrRunCredentialOwner = errors.New("credential handoff requires the original invocation principal")

// RunCredentialTarget contains retained, non-secret identities. It authorizes
// transport only when ClaimedNow was committed by this caller's transaction.
type RunCredentialTarget struct {
	atc.RunCredentialSession
	HandoffID  output.HandoffID
	NodeName   string
	Start      *executioncontrol.Acknowledgement
	ClaimedNow bool
	// WorkerImage is the result producer's image as the Run snapshotted it
	// (atc.RunTaskImage): empty unless rootfs_uri is its Pod's only image.
	WorkerImage string
}

// LoadRunCredentialTarget rechecks the invocation owner, then follows the
// existing named-result producer to its execution. It never invents an executor
// from a mutable build plan and never retains credential contents.
func LoadRunCredentialTarget(ctx context.Context, tx Tx, templateID, number int, principal, result string, epoch int64, claim bool) (RunCredentialTarget, error) {
	target := RunCredentialTarget{RunCredentialSession: atc.RunCredentialSession{Result: result, Status: "waiting"}}
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT r.id,i.principal_digest FROM pipeline_runs r JOIN pipeline_run_invocations i ON i.run_id=r.id
 WHERE r.template_pipeline_id=$1 AND r.number=$2 AND r.run_contract_version='v2'`, templateID, number).Scan(&target.RunID, &owner)
	if err != nil {
		return target, err
	}
	if owner != principal || principal == "" {
		return target, ErrRunCredentialOwner
	}
	run, err := lockRunResultPublication(ctx, tx, target.RunID)
	if err != nil {
		return target, err
	}
	var storedResult string
	var ready bool
	err = tx.QueryRowContext(ctx, `SELECT h.handoff_id,s.result_name,h.ready_at IS NOT NULL FROM pipeline_run_credential_handoffs h
 JOIN pipeline_run_output_starts s USING(handoff_id) WHERE h.run_id=$1`, target.RunID).Scan(&target.HandoffID, &storedResult, &ready)
	if err == nil {
		if storedResult != result {
			return target, output.ErrConflict
		}
		target.Status = "claimed"
		if ready {
			target.Status = "ready"
		}
		return target, nil
	}
	if err != sql.ErrNoRows {
		return target, err
	}
	// The activation rows are already held by lockRunResultPublication. New
	// delivery requires enabled/current activation; replay above retains facts
	// even while an epoch drains or the Run has completed.
	if err := lockRunActivation(ctx, tx, epoch); err != nil {
		return target, err
	}
	if run.ActivationEpoch() != epoch {
		return target, atc.ErrRunResultsUnavailable
	}
	if run.CancellationRequested() {
		return target, ErrPipelineRunCancelling
	}
	if run.Status() != atc.RunStatusRunning {
		return target, ErrPipelineRunNotRunning
	}
	definition, found, err := readRunDefinition(tx, target.RunID)
	if err != nil {
		return target, err
	}
	if !found {
		return target, output.ErrIncomplete
	}
	declarations, err := atc.RunTaskDeclarations(definition.Materialized)
	if err != nil {
		return target, err
	}
	var taskID string
	for _, d := range declarations {
		if d.Result != nil && d.Result.Name == result {
			taskID = d.TaskID
			break
		}
	}
	if taskID == "" {
		return target, output.ErrInvalidIdentity
	}
	target.WorkerImage = atc.RunTaskImage(definition.Materialized, taskID)
	// Refuse ambiguity instead of choosing a different attempt to receive the
	// owner's credential. The initial review template has one result producer.
	rows, err := tx.QueryContext(ctx, `SELECT e.handoff_id,e.node_name,e.node_uid,e.execution_id,e.execution_fence,e.activation_epoch,
 b.aborted,b.completed,b.status,EXISTS(SELECT 1 FROM pipeline_run_execution_closures c WHERE c.execution_id=e.execution_id AND c.execution_fence=e.execution_fence)
 FROM pipeline_run_output_starts s JOIN pipeline_run_executions e ON e.handoff_id=s.handoff_id AND e.run_id=s.run_id AND e.build_id=s.build_id
 JOIN builds b ON b.id=e.build_id AND b.pipeline_run_id=e.run_id
 WHERE s.run_id=$1 AND s.task_id=$2 AND s.result_name=$3 AND e.kind='task'`, target.RunID, taskID, result)
	if err != nil {
		return target, err
	}
	var identity executioncontrol.Identity
	var uid, status string
	var actualEpoch int64
	var aborted, completed, closed bool
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&target.HandoffID, &target.NodeName, &uid, &identity.ExecutionID, &identity.Fence, &actualEpoch, &aborted, &completed, &status, &closed); err != nil {
			rows.Close()
			return target, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return target, err
	}
	if count == 0 {
		return target, nil
	}
	if count != 1 || actualEpoch != epoch || aborted || completed || closed || (status != "pending" && status != "started") {
		return target, output.ErrConflict
	}
	target.Start, err = retainedRunExecutionStart(ctx, tx, identity)
	if err != nil {
		return target, err
	}
	if target.Start == nil {
		return target, nil
	}
	if string(target.Start.NodeUID) != uid || int64(target.Start.ActivationEpoch) != epoch {
		return target, output.ErrInvalidIdentity
	}
	target.Status = "available"
	if claim {
		if _, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_credential_handoffs(run_id,handoff_id) VALUES($1,$2)`, target.RunID, string(target.HandoffID)); err != nil {
			return target, err
		}
		target.ClaimedNow, target.Status = true, "claimed"
	}
	return target, nil
}

// RecordRunCredentialReady records the completed handoff fact, including when
// cancellation raced delivery. It grants no new authority and accepts no bytes.
func RecordRunCredentialReady(ctx context.Context, tx Tx, runID int, handoff output.HandoffID) error {
	if _, err := lockRunResultPublication(ctx, tx, runID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_run_credential_handoffs SET ready_at=coalesce(ready_at,clock_timestamp()) WHERE run_id=$1 AND handoff_id=$2`, runID, string(handoff))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return output.ErrInvalidIdentity
	}
	return nil
}
