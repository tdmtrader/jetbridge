package db

import (
	"context"
	"database/sql"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// RunExecution is a read-only recovery lookup. A retained identity remains
// readable after cancellation; only AdmitRunExecution grants start admission.
func (f *pipelineRunFactory) RunExecution(ctx context.Context, tx Tx, buildID int, planID atc.PlanID) (RunExecutionAdmission, bool, error) {
	var a RunExecutionAdmission
	err := tx.QueryRowContext(ctx, `SELECT run_id,build_id,plan_id,kind,activation_epoch,node_name,node_uid,execution_id,execution_fence,coalesce(handoff_id::text,'')
 FROM pipeline_run_executions WHERE build_id=$1 AND plan_id=$2`, buildID, planID).
		Scan(&a.RunID, &a.BuildID, &a.PlanID, &a.Kind, &a.Epoch, &a.NodeName, &a.NodeUID, &a.Identity.ExecutionID, &a.Identity.Fence, &a.HandoffID)
	if err == sql.ErrNoRows {
		return a, false, nil
	}
	return a, err == nil, err
}

// RunExecutionOwner discovers whether this build needs v2 admission. It grants
// nothing; admission subsequently rechecks ownership and the fence under lock.
func (f *pipelineRunFactory) RunExecutionOwner(ctx context.Context, tx Tx, buildID int) (int, bool, error) {
	var runID int
	err := tx.QueryRowContext(ctx, `SELECT r.id FROM builds b JOIN pipeline_runs r ON r.id=b.pipeline_run_id
 WHERE b.id=$1`, buildID).Scan(&runID)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return runID, err == nil, err
}

// RunExecutionContainer follows the actual container owner. Display metadata
// may be absent on older rows and must not authorize an untracked writer.
func (f *pipelineRunFactory) RunExecutionContainer(ctx context.Context, tx Tx, handle string) (bool, error) {
	var owned bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM containers c JOIN builds b ON b.id=c.build_id
 JOIN pipeline_runs r ON r.id=b.pipeline_run_id WHERE c.handle=$1)`, handle).Scan(&owned)
	return owned, err
}

func (f *pipelineRunFactory) AdmitRunExecution(ctx context.Context, tx Tx, req RunExecutionRequest) (RunExecutionAdmission, bool, error) {
	var a RunExecutionAdmission
	runID, owned, err := f.RunExecutionOwner(ctx, tx, req.BuildID)
	if err != nil || !owned {
		return a, owned, err
	}
	if len(req.PlanID) == 0 || len(req.PlanID) > 256 || req.NodeName == "" || req.NodeUID == "" {
		return a, true, fmt.Errorf("%w: incomplete Run execution identity", output.ErrInvalidIdentity)
	}
	switch req.Kind {
	case ContainerTypeTask, ContainerTypeGet, ContainerTypePut, ContainerTypeCheck:
	default:
		return a, true, output.ErrInvalidIdentity
	}
	if err = lockRunExecutionBuild(ctx, tx, runID, req.BuildID, req.Epoch); err != nil {
		return a, true, err
	}
	a, found, err := f.RunExecution(ctx, tx, req.BuildID, req.PlanID)
	if err != nil {
		return a, true, err
	}
	if found {
		if a.RunID != runID || a.RunExecutionRequest != req {
			return a, true, fmt.Errorf("%w: Run execution already bound to another admission", output.ErrConflict)
		}
		return a, true, nil
	}
	a = RunExecutionAdmission{RunExecutionRequest: req, RunID: runID, Identity: executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1}}
	if req.HandoffID != "" {
		if req.Kind != ContainerTypeTask {
			return a, true, output.ErrInvalidIdentity
		}
		// A selected-output execution already has its exact identity. Linking it
		// must never create a competing identity for the same producer.
		err = tx.QueryRowContext(ctx, `SELECT h.execution_id,h.execution_fence FROM pipeline_run_output_starts s
 JOIN hangar_handoff_predeclarations h USING(handoff_id)
 WHERE s.handoff_id=$1 AND s.run_id=$2 AND s.build_id=$3 AND s.node_name=$4 AND s.node_uid=$5
 AND h.activation_epoch=$6 AND NOT run_output_closed(s.handoff_id)`, string(req.HandoffID), runID, req.BuildID, req.NodeName, req.NodeUID, req.Epoch).Scan(&a.Identity.ExecutionID, &a.Identity.Fence)
		if err == sql.ErrNoRows {
			return a, true, output.ErrInvalidIdentity
		}
		if err != nil {
			return a, true, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_executions
 (run_id,build_id,plan_id,kind,activation_epoch,node_name,node_uid,execution_id,execution_fence,handoff_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,nullif($10,'')::uuid)`, a.RunID, a.BuildID, a.PlanID, a.Kind, a.Epoch, a.NodeName, a.NodeUID, string(a.Identity.ExecutionID), int64(a.Identity.Fence), string(a.HandoffID))
	return a, true, err
}

// lockRunExecutionBuild admits an exact execution: the Run continues under its
// own birth epoch, and the execution is controlled under the Hangar epoch this
// control plane speaks for now, which must be enabled.
func lockRunExecutionBuild(ctx context.Context, tx Tx, runID, buildID int, epoch int64) error {
	var teamID int
	var runEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT p.team_id, r.activation_epoch FROM pipeline_runs r JOIN pipelines p ON p.id=r.template_pipeline_id WHERE r.id=$1`, runID).Scan(&teamID, &runEpoch); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 FOR SHARE`, teamID).Scan(&teamID); err != nil {
		return err
	}
	if err := lockRunContinuation(ctx, tx, runEpoch); err != nil {
		return err
	}
	if err := lockEnabledHangarEpoch(ctx, tx, epoch); err != nil {
		return err
	}
	run := &pipelineRun{}
	if err := scanPipelineRun(run, pipelineRunsQuery.Where(sq.Eq{"r.id": runID}).Suffix("FOR NO KEY UPDATE OF r").RunWith(tx).QueryRowContext(ctx)); err != nil {
		return err
	}
	if run.ActivationEpoch() != runEpoch {
		return atc.ErrRunResultsUnavailable
	}
	if run.CancellationRequested() {
		return ErrPipelineRunCancelling
	}
	if run.Status() != atc.RunStatusRunning {
		return ErrPipelineRunNotRunning
	}
	payload, found := run.InstancePipelineID()
	if !found {
		return ErrPipelineRunPayloadGone
	}
	var id int
	if err := tx.QueryRowContext(ctx, `SELECT id FROM pipelines WHERE id=$1 FOR NO KEY UPDATE`, payload).Scan(&id); err != nil {
		return err
	}
	// Job rows precede builds in the global order. Checks have no job row.
	if _, err := tx.ExecContext(ctx, `SELECT id FROM jobs WHERE id=(SELECT job_id FROM builds WHERE id=$1) FOR UPDATE`, buildID); err != nil {
		return err
	}
	var actualRun, pipelineID int
	var aborted, completed bool
	var status string
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(pipeline_run_id,0),pipeline_id,aborted,completed,status FROM builds WHERE id=$1 FOR UPDATE`, buildID).Scan(&actualRun, &pipelineID, &aborted, &completed, &status); err != nil {
		return err
	}
	if actualRun != runID || pipelineID != payload {
		return output.ErrInvalidIdentity
	}
	if aborted || completed || (status != "pending" && status != "started") {
		return fmt.Errorf("%w: build start admission is closed", output.ErrConflict)
	}
	return nil
}
