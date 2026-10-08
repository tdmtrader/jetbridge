package db

// The Run's side of a capture.
//
// Hangar's capture row names an execution and an output and no Run; this side
// says which named result of which task in which build that capture produces.
// The two are written in one transaction when the producing task is admitted,
// before its Pod exists, and neither changes afterwards: the capture row moves
// through its states under the capture coordinator, and this row only ever
// reads them.

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

// RunCapture is a snapshot of one producer's capture, never permission to
// start a Pod. Mutations revalidate ownership under the Run lock.
type RunCapture struct {
	RunID      int
	BuildID    int
	TaskID     string
	TaskName   string
	ResultName string
	Capture    output.Capture
}

// Key names the capture.
func (c RunCapture) Key() output.CaptureKey { return c.Capture.Key }

func runCaptureOutputRepository() *HangarOutputRepository {
	prefix, err := HangarConsumerPrefixHeld("pipeline-run-capture")
	if err != nil {
		panic(err)
	}

	return NewHangarOutputRepository(prefix)
}

// RunCaptureTask reads the capture a task of a build produces, if any.
func (f *pipelineRunFactory) RunCaptureTask(ctx context.Context, tx Tx, buildID int, taskID string) (RunCapture, bool, error) {
	var c RunCapture
	var execution, outputName string
	err := tx.QueryRowContext(ctx, `SELECT run_id, build_id, task_id, task_name, result_name, execution_id, output_name
		FROM pipeline_run_captures WHERE build_id=$1 AND task_id=$2`, buildID, taskID).
		Scan(&c.RunID, &c.BuildID, &c.TaskID, &c.TaskName, &c.ResultName, &execution, &outputName)
	if err == sql.ErrNoRows {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	c.Capture, err = runCaptureOutputRepository().GetCapture(ctx, tx, output.CaptureKey{
		ExecutionID: executioncontrol.ExecutionID(execution), Output: output.OutputName(outputName),
	})

	return c, err == nil, err
}

// StartRunCapture is step 1's database half for a Run producer: under the
// Run boundary, a pending capture row and the Run's link to it, in the
// caller's transaction. A replay returns the original exact execution; the
// same producer on another node or for another result is a conflict.
func (f *pipelineRunFactory) StartRunCapture(ctx context.Context, tx Tx, buildID int, plan atc.TaskPlan, term time.Duration, nodeName, nodeUID string) (RunCapture, error) {
	if nodeName == "" || nodeUID == "" || term <= 0 {
		return RunCapture{}, fmt.Errorf("%w: missing node identity or invalid capture term", output.ErrIncomplete)
	}
	runID, err := f.lockOutputProducer(ctx, tx, buildID, plan)
	if err != nil {
		return RunCapture{}, err
	}
	existing, found, err := f.RunCaptureTask(ctx, tx, buildID, plan.TaskID)
	if err != nil {
		return RunCapture{}, err
	}
	if found {
		if existing.RunID != runID || existing.ResultName != plan.RunResult.Name || existing.TaskName != plan.Name ||
			string(existing.Capture.Key.Output) != plan.RunResult.Output ||
			existing.Capture.Node != nodeName || string(existing.Capture.NodeUID) != nodeUID {
			return RunCapture{}, fmt.Errorf("%w: producer capture already names another result, task or node", output.ErrConflict)
		}

		return existing, nil
	}

	capture, err := runCaptureOutputRepository().InsertPending(ctx, tx, output.PendingCapture{
		Execution: executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1},
		Output:    output.OutputName(plan.RunResult.Output),
		Node:      nodeName,
		NodeUID:   executioncontrol.NodeUID(nodeUID),
		Term:      term,
	})
	if err != nil {
		return RunCapture{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_captures
		(run_id, build_id, task_id, result_name, task_name, execution_id, output_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, runID, buildID, plan.TaskID, plan.RunResult.Name, plan.Name,
		string(capture.Key.ExecutionID), string(capture.Key.Output)); err != nil {
		return RunCapture{}, err
	}

	return RunCapture{
		RunID: runID, BuildID: buildID, TaskID: plan.TaskID, TaskName: plan.Name,
		ResultName: plan.RunResult.Name, Capture: capture,
	}, nil
}

// lockOutputProducer takes the Run's domain prefix for a producer and checks
// it is still admitted: team, activation, Run, payload and build, in that
// order, then the retained definition.
func (f *pipelineRunFactory) lockOutputProducer(ctx context.Context, tx Tx, buildID int, plan atc.TaskPlan) (int, error) {
	id, err := uuid.Parse(plan.TaskID)
	if err != nil || id == uuid.Nil || id.String() != plan.TaskID || plan.RunResult == nil {
		return 0, fmt.Errorf("%w: task has no stable result declaration", output.ErrInvalidIdentity)
	}
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
	// The Run continues under its own epoch; the capture is admitted by an
	// output plane in service.
	if err := lockRunContinuation(ctx, tx, runEpoch); err != nil {
		return 0, err
	}
	if err := lockEnabledHangarOutput(ctx, tx); err != nil {
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
	payload, found := run.PayloadID()
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline_run_captures s
		JOIN hangar_captures h USING (execution_id, output_name)
		WHERE s.build_id=$1 AND s.task_id=$2 AND h.state <> 'pending')`, buildID, plan.TaskID).Scan(&closed); err != nil {
		return 0, err
	}
	if closed {
		return 0, fmt.Errorf("%w: Run producer capture is already past its start", output.ErrConflict)
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

// CaptureProgress is what a watcher of a Run sees about its captures: the
// announcements a capture row's state implies, in the order it reached them.
// The authorized caller selects the Run; the result contains no execution,
// node, tree ref, claim or grant.
func (f *pipelineRunFactory) CaptureProgress(ctx context.Context, runID int) ([]atc.RunCaptureProgress, error) {
	rows, err := f.conn.QueryContext(ctx, `SELECT s.task_id, s.build_id, s.result_name, h.state, coalesce(h.error, '')
		FROM pipeline_run_captures s JOIN hangar_captures h USING (execution_id, output_name)
		WHERE s.run_id=$1 ORDER BY s.build_id, s.task_id`, runID)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var progress []atc.RunCaptureProgress
	for rows.Next() {
		var item atc.RunCaptureProgress
		var state, reason string
		if err := rows.Scan(&item.TaskID, &item.BuildID, &item.Result, &state, &reason); err != nil {
			return nil, err
		}
		item.Events = CaptureAnnouncements(output.CaptureState(state), reason)
		progress = append(progress, item)
	}

	return progress, rows.Err()
}

// Capture announcement kinds: the only things a watcher sees about a capture.
const (
	CaptureSelected    = "capture-selected"
	CaptureSealStarted = "capture-seal-started"
	CaptureSettled     = "capture-disposition"
)

// CaptureAnnouncements is the announcement history a capture row's state
// implies. Selection is the row's existence (post-completion hijack is gone);
// the seal is publishing; the disposition is a terminal state with its reason.
func CaptureAnnouncements(state output.CaptureState, reason string) []atc.RunCaptureEvent {
	events := []atc.RunCaptureEvent{{Kind: CaptureSelected}}
	switch state {
	case output.CapturePublishing:
		events = append(events, atc.RunCaptureEvent{Kind: CaptureSealStarted})
	case output.CapturePublished:
		events = append(events, atc.RunCaptureEvent{Kind: CaptureSealStarted},
			atc.RunCaptureEvent{Kind: CaptureSettled, Disposition: "capture", Reason: "captured"})
	case output.CaptureFailed:
		events = append(events, atc.RunCaptureEvent{Kind: CaptureSettled, Disposition: "capture", Reason: reason})
	case output.CaptureDiscarded:
		events = append(events, atc.RunCaptureEvent{Kind: CaptureSettled, Disposition: "no_capture", Reason: reason})
	}

	return events
}
