package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// The Run contract has its own activation marker, independent of the Hangar
// output epoch (durable Run contract, amendment M-2 decision 3). Every Run
// transaction locks it first, before any Run-domain lock; a transaction that
// also needs a Hangar epoch locks that epoch's row next, still inside the
// activation prefix.

// runActivationMarker is the Run activation marker as a transaction locked it.
type runActivationMarker struct {
	epoch   int64
	enabled bool
}

// admits is admission's test: the marker must admit, at exactly the epoch the
// new Run is born under.
func (m runActivationMarker) admits(epoch int64) error {
	if epoch <= 0 || !m.enabled || m.epoch != epoch {
		return atc.ErrRunResultsUnavailable
	}
	return nil
}

// continues is the test for work on a Run that already exists: the marker
// must not have been downgraded below the Run's birth epoch. It need not
// admit -- turning admission off stops new Runs, not running ones -- and it is
// indifferent to any Hangar epoch rotation.
func (m runActivationMarker) continues(runEpoch int64) error {
	if runEpoch <= 0 || m.epoch < runEpoch {
		return atc.ErrRunResultsUnavailable
	}
	return nil
}

// lockRunContinuation is the prefix for work on a Run that already exists.
func lockRunContinuation(ctx context.Context, tx Tx, runEpoch int64) error {
	marker, err := lockRunActivationMarker(ctx, tx)
	if err != nil {
		return err
	}
	return marker.continues(runEpoch)
}

func lockRunActivationMarker(ctx context.Context, tx Tx) (runActivationMarker, error) {
	var marker runActivationMarker
	err := tx.QueryRowContext(ctx, `SELECT epoch, admission_enabled FROM pipeline_run_activation WHERE singleton FOR SHARE`).Scan(&marker.epoch, &marker.enabled)
	return marker, err
}

// lockEnabledHangarEpoch requires the Hangar epoch new capture or input work
// speaks for to be enabled on both facets.
func lockEnabledHangarEpoch(ctx context.Context, tx Tx, epoch int64) error {
	if epoch <= 0 {
		return atc.ErrRunResultsUnavailable
	}
	ready, err := hangarLockEnabledEpoch(ctx, tx, epoch)
	if err != nil {
		return err
	}
	if !ready {
		return atc.ErrRunResultsUnavailable
	}
	return nil
}

// PredeclareOutputTask is Stage 1's database half. The caller owns the
// transaction, including the handoff and its Run binding. No daemon, Pod or
// process operation runs here. A replay returns the original exact execution.
func (f *pipelineRunFactory) PredeclareOutputTask(ctx context.Context, tx Tx, buildID int, plan atc.TaskPlan, epoch int64, term time.Duration, nodeName, nodeUID string) (output.HandoffRecord, error) {
	if nodeName == "" || nodeUID == "" || term <= 0 {
		return output.HandoffRecord{}, fmt.Errorf("%w: missing node identity or invalid capture term", output.ErrIncomplete)
	}
	runID, err := f.lockOutputProducer(ctx, tx, buildID, plan, epoch)
	if err != nil {
		return output.HandoffRecord{}, err
	}
	repository := runOutputRepository()
	var handoff, result, taskName, node, uid string
	err = tx.QueryRowContext(ctx, `SELECT handoff_id, result_name, task_name, node_name, node_uid
		FROM pipeline_run_output_starts WHERE run_id=$1 AND build_id=$2 AND task_id=$3`, runID, buildID, plan.TaskID).
		Scan(&handoff, &result, &taskName, &node, &uid)
	if err == nil {
		if result != plan.RunResult.Name || taskName != plan.Name || node != nodeName || uid != nodeUID {
			return output.HandoffRecord{}, fmt.Errorf("%w: producer start already names another result, task or node", output.ErrConflict)
		}
		return repository.LoadHandoffRecord(ctx, tx, output.HandoffID(handoff))
	}
	if err != sql.ErrNoRows {
		return output.HandoffRecord{}, err
	}

	admission := output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1},
		ActivationEpoch: executioncontrol.ActivationEpoch(epoch),
		HandoffID:       output.HandoffID(uuid.NewString()),
		SourceHoldID:    output.SourceHoldID(uuid.NewString()),
		Output:          output.OutputName(plan.RunResult.Output),
	}
	// PostgreSQL 14 parses an interval's integer microseconds as int32, so
	// even a one-hour capture overflows. Decimal seconds preserve its precision.
	seconds := fmt.Sprintf("%d.%06d", term/time.Second, term%time.Second/time.Microsecond)
	if err := tx.QueryRowContext(ctx, `SELECT now() + $1::numeric * interval '1 second'`, seconds).Scan(&admission.CaptureDeadline.Time); err != nil {
		return output.HandoffRecord{}, err
	}
	if err := repository.PredeclareHandoff(ctx, tx, admission); err != nil {
		return output.HandoffRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_output_starts
		(run_id, build_id, task_id, result_name, task_name, node_name, node_uid, handoff_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, runID, buildID, plan.TaskID, plan.RunResult.Name, plan.Name, nodeName, nodeUID, string(admission.HandoffID)); err != nil {
		return output.HandoffRecord{}, err
	}
	return repository.LoadHandoffRecord(ctx, tx, admission.HandoffID)
}

// RequestOutputSource commits dispatch intent before the external reservation.
// Cancellation may close an undispatched start immediately; an admitted attempt
// must first be reconciled against its exact daemon, even if its reply was lost.
func (f *pipelineRunFactory) RequestOutputSource(ctx context.Context, tx Tx, buildID int, plan atc.TaskPlan, epoch int64) error {
	runID, err := f.lockOutputProducer(ctx, tx, buildID, plan, epoch)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_run_output_starts
		SET source_requested_at=coalesce(source_requested_at,now())
		WHERE run_id=$1 AND build_id=$2 AND task_id=$3 AND task_name=$4 AND result_name=$5`, runID, buildID, plan.TaskID, plan.Name, plan.RunResult.Name)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: source dispatch has no matching Run start", output.ErrInvalidIdentity)
	}
	return nil
}

// RecordOutputSource runs after the network reservation, in a fresh transaction
// under the same Run boundary. An ambiguous commit can be retried with the same
// daemon-issued reservation. It cannot move an admitted execution to another node.
func (f *pipelineRunFactory) RecordOutputSource(ctx context.Context, tx Tx, buildID int, plan atc.TaskPlan, reserved output.ReservedIncarnation, nodeName string) error {
	if err := reserved.Validate(); err != nil {
		return err
	}
	if plan.RunResult == nil {
		return fmt.Errorf("%w: source has no result producer", output.ErrInvalidIdentity)
	}
	_, owner, err := lockRunOutputHandoff(ctx, tx, reserved.HandoffID)
	if err != nil {
		return err
	}
	if owner == nil || owner.buildID != buildID || owner.completed || owner.epoch != int64(reserved.ActivationEpoch) {
		return fmt.Errorf("%w: source has no active owning Run producer", output.ErrInvalidIdentity)
	}
	var handoff, result, taskName, node, uid string
	var requested bool
	if err := tx.QueryRowContext(ctx, `SELECT handoff_id, result_name, task_name, node_name, node_uid, source_requested_at IS NOT NULL
		FROM pipeline_run_output_starts WHERE run_id=$1 AND build_id=$2 AND task_id=$3`, owner.runID, buildID, plan.TaskID).
		Scan(&handoff, &result, &taskName, &node, &uid, &requested); err != nil {
		return fmt.Errorf("%w: source has no Run start admission", output.ErrInvalidIdentity)
	}
	if !requested || handoff != string(reserved.HandoffID) || result != plan.RunResult.Name || taskName != plan.Name || node != nodeName || uid != string(reserved.Incarnation.NodeUID) {
		return fmt.Errorf("%w: source does not belong to this Run producer and node", output.ErrInvalidIdentity)
	}
	var closed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline_run_output_finishes WHERE handoff_id=$1)`, handoff).Scan(&closed); err != nil {
		return err
	}
	if closed {
		return fmt.Errorf("%w: source reservation arrived after Run disposition", output.ErrConflict)
	}
	// A cancellation request does not erase a previously admitted dispatch.
	// Recording its actual location enables exact release; it permits no Pod
	// or process start, which still passes the stricter start-admission gate.
	return runOutputRepository().RecordSourceReservation(ctx, tx, reserved, nodeName)
}

func (f *pipelineRunFactory) lockOutputProducer(ctx context.Context, tx Tx, buildID int, plan atc.TaskPlan, epoch int64) (int, error) {
	id, err := uuid.Parse(plan.TaskID)
	if err != nil || id == uuid.Nil || id.String() != plan.TaskID || plan.RunResult == nil {
		return 0, fmt.Errorf("%w: task has no stable result declaration", output.ErrInvalidIdentity)
	}
	// Identity discovery has no row locks. Revalidate the build after acquiring
	// team, activation, Run, payload and build in that order. The retained
	// definition supplies producer identity; post-creation work never reaches
	// back to lock the base template.
	var runID, teamID int
	var runEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT r.id, p.team_id, r.activation_epoch FROM builds b
		JOIN pipeline_runs r ON r.id=b.pipeline_run_id
		JOIN pipelines p ON p.id=r.template_pipeline_id WHERE b.id=$1`, buildID).Scan(&runID, &teamID, &runEpoch); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("%w: build has no owning Run", output.ErrInvalidIdentity)
		}
		return 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 FOR SHARE`, teamID).Scan(&teamID); err != nil {
		return 0, err
	}
	// The Run continues under its own epoch; the capture is admitted under
	// the Hangar epoch this control plane speaks for now.
	if err := lockRunContinuation(ctx, tx, runEpoch); err != nil {
		return 0, err
	}
	if err := lockEnabledHangarEpoch(ctx, tx, epoch); err != nil {
		return 0, err
	}
	run := &pipelineRun{}
	err = scanPipelineRun(run, pipelineRunsQuery.Where(sq.Eq{"r.id": runID}).Suffix("FOR NO KEY UPDATE OF r").RunWith(tx).QueryRowContext(ctx))
	if err != nil {
		return 0, err
	}
	if run.ActivationEpoch() != runEpoch {
		return 0, atc.ErrRunResultsUnavailable
	}
	if run.CancellationRequested() {
		return 0, ErrPipelineRunCancelling
	}
	if run.Status() != atc.RunStatusRunning {
		return 0, ErrPipelineRunNotRunning
	}
	payload, found := run.InstancePipelineID()
	if !found {
		return 0, ErrPipelineRunPayloadGone
	}
	var lockedID int
	if err := tx.QueryRowContext(ctx, `SELECT id FROM pipelines WHERE id=$1 FOR NO KEY UPDATE`, payload).Scan(&lockedID); err != nil {
		return 0, err
	}
	var actualRunID, pipelineID int
	var jobName, status string
	var aborted, completed bool
	if err := tx.QueryRowContext(ctx, `SELECT pipeline_run_id, pipeline_id, coalesce(run_job_name,''), status, aborted, completed FROM builds WHERE id=$1 FOR UPDATE`, buildID).
		Scan(&actualRunID, &pipelineID, &jobName, &status, &aborted, &completed); err != nil {
		return 0, err
	}
	if actualRunID != runID || pipelineID != payload || aborted || completed || (status != "pending" && status != "started") {
		return 0, fmt.Errorf("%w: producer build is no longer admitted", output.ErrInvalidIdentity)
	}
	var closed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline_run_output_starts s
		JOIN pipeline_run_output_finishes f USING(handoff_id) WHERE s.build_id=$1 AND s.task_id=$2)`, buildID, plan.TaskID).Scan(&closed); err != nil {
		return 0, err
	}
	if closed {
		return 0, fmt.Errorf("%w: Run producer start is closed by its finish decision", output.ErrConflict)
	}
	definition, found, err := readRunDefinition(tx, runID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("%w: missing retained Run definition", output.ErrIncomplete)
	}
	declarations, err := atc.RunTaskDeclarations(definition.Materialized)
	if err != nil {
		return 0, err
	}
	for _, declared := range declarations {
		if declared.TaskID == plan.TaskID && declared.JobName == jobName && declared.Result != nil && *declared.Result == *plan.RunResult {
			return runID, nil
		}
	}
	return 0, fmt.Errorf("%w: producer does not match the retained Run definition", output.ErrInvalidIdentity)
}

func runOutputRepository() *HangarOutputRepository {
	prefix, err := HangarConsumerPrefixHeld("pipeline-run-output")
	if err != nil {
		panic(err)
	}
	return NewHangarOutputRepository(prefix)
}
