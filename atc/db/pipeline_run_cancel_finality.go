package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// ExecuteCancellationFinality owns the database-only final actions. Build
// completion reuses Finish, and Run publication reuses the sole terminalizer.
// Its caller holds no transaction; notifications happen after commit.
func (f *pipelineRunFactory) ExecuteCancellationFinality(ctx context.Context, lease RunCancellationLease, op RunCancellationOperation) (RunCancellationDebt, error) {
	switch op.Kind {
	case CancelBuild, CancelCandidate, CancelTerminalize, CancelHandoff:
	case CancelCapture, CancelSourceHold:
		// Kinds of the handoff era. Nothing discovers them any more; an
		// operation recorded before the upgrade has nothing left to do.
		return CancellationDone, nil
	default:
		return CancellationUnavailable, ErrRunCancellationExternalWork
	}
	commit := &runCancellationCommit{factory: f, lease: lease, op: op}
	ready, err := commit.perform(ctx)
	if errors.Is(err, atc.ErrRunOutputPending) || (err == nil && !ready) {
		return CancellationPending, nil
	}
	if err != nil {
		err = HangarCommitError(err)
		if errors.Is(err, ErrRunCancellationProgressStale) || errors.Is(err, ErrRunCancellationLeaseLost) || errors.Is(err, output.ErrConflict) || errors.Is(err, output.ErrInvalidIdentity) || errors.Is(err, executioncontrol.ErrStaleFence) {
			return CancellationConflict, err
		}
		return CancellationUnavailable, err
	}
	return CancellationDone, nil
}

type runCancellationCommit struct {
	factory *pipelineRunFactory
	lease   RunCancellationLease
	op      RunCancellationOperation
}

func (c *runCancellationCommit) check(ctx context.Context, tx Tx) error {
	return c.factory.CheckCancellationOperation(ctx, tx, c.lease, c.op)
}

func (c *runCancellationCommit) perform(ctx context.Context) (bool, error) {
	f, op := c.factory, c.op
	if op.Kind == CancelBuild {
		id, err := strconv.Atoi(op.Subject)
		if err != nil || id <= 0 || strconv.Itoa(id) != op.Subject {
			return false, output.ErrInvalidIdentity
		}
		build := newEmptyBuild(f.conn, f.lockFactory)
		if err := scanBuild(build, buildsQuery.Where(sq.Eq{"b.id": id}).RunWith(f.conn).QueryRowContext(ctx), f.conn.EncryptionStrategy()); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return c.collectedBuild(ctx)
			}
			return false, err
		}
		return true, build.finish(ctx, BuildStatusAborted, c)
	}
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	var ready bool
	switch op.Kind {
	case CancelTerminalize:
		if op.Subject != strconv.Itoa(op.RunID) {
			return false, output.ErrInvalidIdentity
		}
		ready, err = f.finalizeOutputRun(ctx, tx, op.RunID, c)
	case CancelHandoff:
		ready, err = c.settleCapture(ctx, tx)
	default:
		ready, err = c.settleCandidate(ctx, tx)
	}
	if err != nil || !ready {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if op.Kind == CancelTerminalize {
		f.AfterRunCompleted()
	}
	return true, nil
}

// collectedBuild settles an abort whose build no longer exists. Outside a team
// purge, which deletes the operation with it, the only thing that deletes a
// Run's build is check collection, and it deletes only a completed check whose
// executions, if any, were all closed. That is a preserved terminal build:
// there is nothing to abort, so the operation is done once it is confirmed
// current.
func (c *runCancellationCommit) collectedBuild(ctx context.Context) (bool, error) {
	tx, err := c.factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	if err := c.lockRun(ctx, tx); err != nil {
		return false, err
	}
	if err := c.check(ctx, tx); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (c *runCancellationCommit) lockRun(ctx context.Context, tx Tx) error {
	run, err := lockRunResultPublication(ctx, tx, c.op.RunID)
	if err != nil {
		return err
	}
	if run.Status() != atc.RunStatusRunning {
		return ErrRunCancellationProgressStale
	}
	if run.CancellationRequested() {
		return nil
	}
	// Without Run cancellation, only an open build closure may finish its own
	// build or discard its own capture. Candidates and terminal publication
	// still need the cancellation.
	var id int
	switch c.op.Kind {
	case CancelBuild:
		parsed, err := strconv.Atoi(c.op.Subject)
		if err != nil {
			return output.ErrInvalidIdentity
		}
		id = parsed
	case CancelHandoff:
		if err := tx.QueryRowContext(ctx, `SELECT build_id FROM pipeline_run_captures
 WHERE run_id=$1 AND execution_id::text||'/'||output_name=$2`, c.op.RunID, c.op.Subject).Scan(&id); err != nil {
			return err
		}
	default:
		return ErrRunCancellationProgressStale
	}
	open, err := buildHasOpenClosure(ctx, tx, id)
	if err != nil {
		return err
	}
	if !open {
		return ErrRunCancellationProgressStale
	}
	return nil
}

// prepareBuild uses the same serialization boundary as every admission. A
// terminal build is preserved. A source or exact execution still owing closure
// prevents both the abort flag and the completion event from being written.
func (c *runCancellationCommit) prepareBuild(ctx context.Context, tx Tx, buildID int) (bool, error) {
	if c.op.Kind != CancelBuild || c.op.Subject != strconv.Itoa(buildID) {
		return false, output.ErrInvalidIdentity
	}
	if err := c.lockRun(ctx, tx); err != nil {
		return false, err
	}
	var completed, closed bool
	var status BuildStatus
	if err := tx.QueryRowContext(ctx, `SELECT completed,status,
 run_execution_closed(b.id) AND NOT EXISTS(SELECT 1 FROM pipeline_run_captures s JOIN hangar_captures h USING(execution_id, output_name)
  WHERE s.build_id=b.id AND h.state IN ('pending','publishing'))
 FROM builds b WHERE id=$1 AND pipeline_run_id=$2`, buildID, c.op.RunID).Scan(&completed, &status, &closed); err != nil {
		return false, err
	}
	if !closed {
		return false, atc.ErrRunOutputPending
	}
	if completed && status != BuildStatusPending && status != BuildStatusStarted {
		return true, nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE builds SET aborted=true WHERE id=$1`, buildID)
	return false, err
}

// settleCapture is a cancellation's whole effect on a capture: a pending one
// is discarded, a publishing one is waited for -- past publishing, a
// cancellation does not undo publication -- and a settled one is done. The
// node's marker is released by the capture coordinator's release pass.
func (c *runCancellationCommit) settleCapture(ctx context.Context, tx Tx) (bool, error) {
	if err := c.lockRun(ctx, tx); err != nil {
		return false, err
	}
	var execution, outputName string
	if err := tx.QueryRowContext(ctx, `SELECT execution_id::text, output_name FROM pipeline_run_captures
 WHERE run_id=$1 AND execution_id::text||'/'||output_name=$2`, c.op.RunID, c.op.Subject).Scan(&execution, &outputName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, output.ErrInvalidIdentity
		}
		return false, err
	}
	key := output.CaptureKey{ExecutionID: executioncontrol.ExecutionID(execution), Output: output.OutputName(outputName)}
	repository := runCaptureOutputRepository()
	capture, err := repository.GetCapture(ctx, tx, key)
	if err != nil {
		return false, err
	}
	switch capture.State {
	case output.CapturePublishing:
		return false, nil
	case output.CapturePending:
		reason := output.DiscardBuildAborted
		var cancelled bool
		if err := tx.QueryRowContext(ctx, `SELECT cancel_requested_at IS NOT NULL FROM pipeline_runs WHERE id=$1`, c.op.RunID).Scan(&cancelled); err != nil {
			return false, err
		}
		if cancelled {
			reason = output.DiscardRunCancelled
		}
		if _, err := repository.CASPendingToDiscarded(ctx, tx, key, reason); err != nil {
			return false, err
		}
	}

	return true, c.check(ctx, tx)
}

func (c *runCancellationCommit) settleCandidate(ctx context.Context, tx Tx) (bool, error) {
	if err := c.lockRun(ctx, tx); err != nil {
		return false, err
	}
	candidates, err := readRunCandidates(ctx, tx, c.op.RunID)
	if err != nil {
		return false, err
	}
	for _, candidate := range candidates {
		if string(candidate.ClaimID) != c.op.Subject {
			continue
		}
		if _, err := lockRunCandidateClaims(ctx, tx, []runCandidate{candidate}); err != nil {
			return false, err
		}
		// The claim remains active and hidden until the atomic aborted
		// publication includes it in the complete release set.
		return true, c.check(ctx, tx)
	}
	return false, output.ErrInvalidIdentity
}

func inspectRunCancellationQuiescence(ctx context.Context, tx Tx, runID, payloadID int) (bool, error) {
	var ready bool
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(
 SELECT 1 FROM builds WHERE pipeline_run_id=$1 AND (NOT completed OR status IN ('pending','started'))
 ) AND NOT EXISTS(SELECT 1 FROM jobs WHERE pipeline_id=$2 AND schedule_requested>last_scheduled)
 AND NOT EXISTS(SELECT 1 FROM pipeline_run_build_closures WHERE run_id=$1 AND closed_at IS NULL)`, runID, payloadID).Scan(&ready)
	return ready, err
}
