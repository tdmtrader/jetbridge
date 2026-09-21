package db

import (
	"context"
	"database/sql"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/output"
)

// RunOutputTask is a snapshot for dispatch, never permission to start a Pod.
// Mutations revalidate ownership under the Run lock in their own transaction.
type RunOutputTask struct {
	BuildID           int
	Plan              atc.TaskPlan
	NodeName, NodeUID string
	Record            output.HandoffRecord
}

func (f *pipelineRunFactory) OutputTask(ctx context.Context, tx Tx, buildID int, taskID string) (RunOutputTask, bool, error) {
	in := RunOutputTask{BuildID: buildID, Plan: atc.TaskPlan{TaskID: taskID, RunResult: &atc.RunResult{}}}
	var handoff string
	err := tx.QueryRowContext(ctx, `SELECT task_name, result_name, node_name, node_uid, handoff_id
		FROM pipeline_run_output_starts WHERE build_id=$1 AND task_id=$2`, buildID, taskID).
		Scan(&in.Plan.Name, &in.Plan.RunResult.Name, &in.NodeName, &in.NodeUID, &handoff)
	if err == sql.ErrNoRows {
		return in, false, nil
	}
	if err != nil {
		return in, false, err
	}
	in.Record, err = runOutputRepository().LoadHandoffRecord(ctx, tx, output.HandoffID(handoff))
	in.Plan.RunResult.Output = string(in.Record.Output)
	return in, err == nil, err
}

// PendingOutputSources includes aborted builds: dispatch authority committed
// before the abort must be reconciled before cancellation can release its source.
func (f *pipelineRunFactory) PendingOutputSources(ctx context.Context, tx Tx, limit int) ([]RunOutputTask, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.build_id,s.task_id FROM pipeline_run_output_starts s
		JOIN hangar_handoff_predeclarations h USING(handoff_id)
		WHERE s.source_requested_at IS NOT NULL AND h.reserved_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM pipeline_run_output_finishes f WHERE f.handoff_id=s.handoff_id)
		ORDER BY s.source_requested_at,s.handoff_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var identities []RunOutputTask
	for rows.Next() {
		var in RunOutputTask
		if err := rows.Scan(&in.BuildID, &in.Plan.TaskID); err != nil {
			rows.Close()
			return nil, err
		}
		identities = append(identities, in)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var pending []RunOutputTask
	for _, id := range identities {
		in, found, err := f.OutputTask(ctx, tx, id.BuildID, id.Plan.TaskID)
		if err != nil {
			return nil, err
		}
		if found {
			pending = append(pending, in)
		}
	}
	return pending, nil
}
