package db

import (
	"context"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/output"
)

// CaptureProgress reads the durable capture announcements in emission order.
// The authorized caller selects the Run; the result contains no handoff, source,
// tree ref, claim or grant. Read the outer rows before using the same connection
// for announcements, preserving the single-connection transaction budget.
func (f *pipelineRunFactory) CaptureProgress(ctx context.Context, runID int) ([]atc.RunCaptureProgress, error) {
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT task_id, build_id, result_name, handoff_id
		FROM pipeline_run_output_starts WHERE run_id=$1 ORDER BY build_id, task_id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var progress []atc.RunCaptureProgress
	var handoffs []output.HandoffID
	for rows.Next() {
		var item atc.RunCaptureProgress
		var handoff string
		if err = rows.Scan(&item.TaskID, &item.BuildID, &item.Result, &handoff); err != nil {
			return nil, err
		}
		progress = append(progress, item)
		handoffs = append(handoffs, output.HandoffID(handoff))
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for i, handoff := range handoffs {
		events, err := runOutputRepository().ReadAnnouncements(ctx, tx, handoff)
		if err != nil {
			return nil, err
		}
		progress[i].Events = make([]atc.RunCaptureEvent, len(events))
		for j, event := range events {
			progress[i].Events[j] = atc.RunCaptureEvent{Kind: event.Kind, Disposition: event.Disposition, Reason: event.Reason}
		}
	}
	return progress, nil
}
