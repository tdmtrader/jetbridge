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
	// Capture is the producer's capture: the credential goes to the
	// execution that produces it.
	Capture    output.CaptureKey
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
 WHERE r.template_pipeline_id=$1 AND r.number=$2`, templateID, number).Scan(&target.RunID, &owner)
	if err != nil {
		return target, err
	}
	if owner != principal || principal == "" {
		return target, ErrRunCredentialOwner
	}
	run, hangarEnabled, err := lockRunResultPublicationUnder(ctx, tx, target.RunID, epoch)
	if err != nil {
		return target, err
	}
	var storedResult string
	var ready bool
	var execution, outputName string
	err = tx.QueryRowContext(ctx, `SELECT h.execution_id,h.output_name,s.result_name,h.ready_at IS NOT NULL FROM pipeline_run_credential_handoffs h
 JOIN pipeline_run_captures s USING(execution_id, output_name) WHERE h.run_id=$1`, target.RunID).Scan(&execution, &outputName, &storedResult, &ready)
	target.Capture = output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(execution), Output: output.OutputName(outputName)}
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
	// The activation rows are already held by lockRunResultPublicationUnder.
	// New delivery grants the owner's credentials to a producer, so it needs
	// the Run contract admitting -- an operator's admission hold stops it, as
	// the HTTP route does -- and the Hangar epoch this control plane speaks for
	// enabled. Replay above retains facts even under a hold, while an epoch
	// drains, or after the Run has completed.
	marker, err := lockRunActivationMarker(ctx, tx)
	if err != nil {
		return target, err
	}
	if !marker.enabled || !hangarEnabled {
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
	rows, err := tx.QueryContext(ctx, `SELECT e.capture_output,e.node_name,e.node_uid,e.execution_id,e.execution_fence,e.activation_epoch,
 b.aborted,b.completed,b.status,EXISTS(SELECT 1 FROM pipeline_run_execution_closures c WHERE c.execution_id=e.execution_id AND c.execution_fence=e.execution_fence)
 FROM pipeline_run_captures s JOIN pipeline_run_executions e ON e.execution_id=s.execution_id AND e.capture_output=s.output_name AND e.run_id=s.run_id AND e.build_id=s.build_id
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
		var captured string
		if err = rows.Scan(&captured, &target.NodeName, &uid, &identity.ExecutionID, &identity.Fence, &actualEpoch, &aborted, &completed, &status, &closed); err != nil {
			rows.Close()
			return target, err
		}
		target.Capture = output.CaptureKey{ExecutionID: identity.ExecutionID, Output: output.OutputName(captured)}
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
		if _, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_credential_handoffs(run_id,execution_id,output_name) VALUES($1,$2,$3)`, target.RunID, string(target.Capture.ExecutionID), string(target.Capture.Output)); err != nil {
			return target, err
		}
		target.ClaimedNow, target.Status = true, "claimed"
	}
	return target, nil
}

// RecordRunCredentialReady records the completed handoff fact, including when
// cancellation raced delivery. It grants no new authority and accepts no bytes.
func RecordRunCredentialReady(ctx context.Context, tx Tx, runID int, capture output.CaptureKey) error {
	if _, err := lockRunResultPublication(ctx, tx, runID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_run_credential_handoffs SET ready_at=coalesce(ready_at,clock_timestamp()) WHERE run_id=$1 AND execution_id=$2 AND output_name=$3`, runID, string(capture.ExecutionID), string(capture.Output))
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
