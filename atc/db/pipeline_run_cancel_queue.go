package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
)

var (
	ErrRunCancellationLimit         = errors.New("invalid cancellation work limit")
	ErrRunCancellationProgressStale = errors.New("cancellation operation is no longer current")
)

// These queries enumerate existing identities, rather than accepting arbitrary
// work from a caller. The subject is resolved again by its owning handler. A
// completed queue row cannot substitute for the owner's durable evidence.
var cancellationSources = []struct {
	kind  RunCancellationKind
	query string
}{
	{CancelSchedulerDebt, `SELECT j.id::text||'/'||(extract(epoch FROM j.schedule_requested)*1000000)::bigint::text AS subject FROM jobs j JOIN pipelines p ON p.id=j.pipeline_id WHERE p.pipeline_run_id=$1 AND j.schedule_requested>j.last_scheduled`},
	{CancelHandoff, `SELECT handoff_id::text AS subject FROM pipeline_run_output_starts WHERE run_id=$1`},
	{CancelCapture, `SELECT c.reservation_id::text AS subject FROM pipeline_run_output_starts s JOIN hangar_capture_reservations c USING(handoff_id) WHERE s.run_id=$1`},
	{CancelSourceHold, `SELECT h.source_hold_id::text AS subject FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id) WHERE s.run_id=$1`},
	{CancelBuild, `SELECT id::text AS subject FROM builds WHERE pipeline_run_id=$1`},
	{CancelExecution, `SELECT h.execution_id::text||'/'||h.execution_fence::text AS subject FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id) WHERE s.run_id=$1
 UNION SELECT execution_id::text||'/'||execution_fence::text AS subject FROM pipeline_run_executions WHERE run_id=$1`},
	{CancelCandidate, `SELECT c.claim_id::text AS subject FROM pipeline_run_output_starts s JOIN pipeline_run_output_candidates c USING(handoff_id) WHERE s.run_id=$1`},
	{CancelTerminalize, `SELECT id::text AS subject FROM pipeline_runs WHERE id=$1`},
}

// PendingRunCancellations advances a finite, persisted global cycle. A failed Run
// cannot pin a page; Runs discovered above its high-water enter the next cycle.
// This transaction acquires only the worker row, never a Run/domain lock.
func (f *pipelineRunFactory) PendingRunCancellations(ctx context.Context, tx Tx, lease RunCancellationLease, limit int) ([]int, error) {
	if limit < 1 || limit > RunCancellationRunLimit {
		return nil, ErrRunCancellationLimit
	}
	if _, _, err := lockCurrentCancellationLease(ctx, tx, lease); err != nil {
		return nil, err
	}
	var after, high int
	if err := tx.QueryRowContext(ctx, `SELECT last_run_id,run_high_water FROM pipeline_run_cancellation_worker WHERE singleton`).Scan(&after, &high); err != nil {
		return nil, err
	}
	for cycle := 0; cycle < 2; cycle++ {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM pipeline_runs WHERE run_contract_version='v2' AND cancel_requested_at IS NOT NULL AND status='running' AND id>$1 AND id<=$2 ORDER BY id LIMIT $3`, after, high, limit)
		if err != nil {
			return nil, err
		}
		var ids []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				Close(rows)
				return nil, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		Close(rows)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			_, err = tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_worker SET last_run_id=$1,run_high_water=$2 WHERE singleton`, ids[len(ids)-1], high)
			return ids, err
		}
		if cycle == 0 {
			after = 0
			if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(id),0) FROM pipeline_runs WHERE run_contract_version='v2' AND cancel_requested_at IS NOT NULL AND status='running'`).Scan(&high); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_worker SET last_run_id=0,run_high_water=$1 WHERE singleton`, high); err != nil {
				return nil, err
			}
		}
	}
	return nil, nil
}

// DiscoverRunCancellation holds the Run boundary only while recording a bounded
// set of typed identities. It never contacts an executor or a storage service.
func (f *pipelineRunFactory) DiscoverRunCancellation(ctx context.Context, tx Tx, lease RunCancellationLease, runID, limit int) (int, error) {
	if limit < 1 || limit > RunCancellationOperationLimit {
		return 0, ErrRunCancellationLimit
	}
	if _, _, err := lockCancellationProgress(ctx, tx, lease, runID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_cancellation_progress(run_id) VALUES($1) ON CONFLICT DO NOTHING`, runID); err != nil {
		return 0, err
	}
	var first int
	if err := tx.QueryRowContext(ctx, `SELECT next_discovery_kind FROM pipeline_run_cancellation_progress WHERE run_id=$1`, runID).Scan(&first); err != nil {
		return 0, err
	}
	count := 0
	for offset := 0; offset < len(cancellationSources) && count < limit; offset++ {
		index := (first + offset) % len(cancellationSources)
		source := cancellationSources[index]
		query := `INSERT INTO pipeline_run_cancellation_operations(run_id,kind,subject)
 SELECT $1,$2,owned.subject FROM (` + source.query + `) owned
 WHERE NOT EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations op WHERE op.run_id=$1 AND op.kind=$2 AND op.subject=owned.subject)
 ORDER BY owned.subject LIMIT $3 ON CONFLICT DO NOTHING`
		result, err := tx.ExecContext(ctx, query, runID, string(source.kind), limit-count)
		if err != nil {
			return 0, err
		}
		added, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		count += int(added)
		if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_progress SET next_discovery_kind=$2 WHERE run_id=$1`, runID, (index+1)%len(cancellationSources)); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// ClaimRunCancellationOperation commits an interrupted retry debt before the
// caller performs work. If the process dies, the same identity becomes due after
// its bounded deadline while the persisted cursor continues to later work.
func (f *pipelineRunFactory) ClaimRunCancellationOperation(ctx context.Context, tx Tx, lease RunCancellationLease, runID int) (RunCancellationOperation, bool, error) {
	now, expires, err := lockCancellationProgress(ctx, tx, lease, runID)
	if err != nil {
		return RunCancellationOperation{}, false, err
	}
	deadline := now.Add(RunCancellationOperationTerm)
	if safe := expires.Add(-time.Second); safe.Before(deadline) {
		deadline = safe
	}
	if !deadline.After(now) {
		return RunCancellationOperation{}, false, ErrRunCancellationLeaseLost
	}
	var first int
	err = tx.QueryRowContext(ctx, `SELECT next_claim_kind FROM pipeline_run_cancellation_progress WHERE run_id=$1`, runID).Scan(&first)
	if errors.Is(err, sql.ErrNoRows) {
		return RunCancellationOperation{}, false, nil
	}
	if err != nil {
		return RunCancellationOperation{}, false, err
	}
	for offset := 0; offset < len(cancellationSources); offset++ {
		index := (first + offset) % len(cancellationSources)
		kind := cancellationSources[index].kind
		if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_cancellation_cursors(run_id,kind) VALUES($1,$2) ON CONFLICT DO NOTHING`, runID, string(kind)); err != nil {
			return RunCancellationOperation{}, false, err
		}
		var after, high int64
		if err := tx.QueryRowContext(ctx, `SELECT after_id,high_water FROM pipeline_run_cancellation_cursors WHERE run_id=$1 AND kind=$2`, runID, string(kind)).Scan(&after, &high); err != nil {
			return RunCancellationOperation{}, false, err
		}
		for cycle := 0; cycle < 2; cycle++ {
			op := RunCancellationOperation{RunID: runID, Kind: kind, WorkerEpoch: lease.Epoch, DatabaseNow: now.UTC(), Deadline: deadline.UTC()}
			err := tx.QueryRowContext(ctx, `SELECT id,subject,attempt_count FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2 AND id>$3 AND id<=$4 AND completed_at IS NULL AND next_at<=$5 ORDER BY id LIMIT 1`, runID, string(kind), after, high, now).Scan(&op.ID, &op.Subject, &op.Attempt)
			if err == nil {
				op.Attempt++
				if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_operations SET attempt_count=$2,worker_epoch=$3,claimed=true,next_at=$4,debt='interrupted' WHERE id=$1`, op.ID, op.Attempt, lease.Epoch, deadline); err != nil {
					return op, false, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_cursors SET after_id=$3 WHERE run_id=$1 AND kind=$2`, runID, string(kind), op.ID); err != nil {
					return op, false, err
				}
				_, err = tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_progress SET next_claim_kind=$2 WHERE run_id=$1`, runID, (index+1)%len(cancellationSources))
				return op, err == nil, err
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return op, false, err
			}
			if cycle == 0 {
				after = 0
				if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(id),0) FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2`, runID, string(kind)).Scan(&high); err != nil {
					return op, false, err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_cursors SET after_id=0,high_water=$3,cycle=cycle+1 WHERE run_id=$1 AND kind=$2`, runID, string(kind), high); err != nil {
					return op, false, err
				}
			}
		}
	}
	return RunCancellationOperation{}, false, nil
}

func (f *pipelineRunFactory) RecordRunCancellationProgress(ctx context.Context, tx Tx, lease RunCancellationLease, op RunCancellationOperation, debt RunCancellationDebt) error {
	switch debt {
	case CancellationDone, CancellationPending, CancellationUnavailable, CancellationTimeout, CancellationConflict:
	default:
		return fmt.Errorf("invalid cancellation retry debt")
	}
	if op.WorkerEpoch != lease.Epoch {
		return ErrRunCancellationProgressStale
	}
	// Publication may have committed before the worker receives its reply.
	// Only that exact terminal operation may finish bookkeeping on the now
	// aborted Run; ordinary work still requires a running cancellation.
	var now time.Time
	var err error
	if op.Kind == CancelTerminalize && op.Subject == strconv.Itoa(op.RunID) {
		err = lockRunCancellationState(ctx, tx, op.RunID, true)
		if err == nil {
			now, _, err = lockCurrentCancellationLease(ctx, tx, lease)
		}
	} else {
		now, _, err = lockCancellationProgress(ctx, tx, lease, op.RunID)
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE pipeline_run_cancellation_operations SET claimed=false,debt=$7,
 completed_at=CASE WHEN $7='' THEN $8::timestamptz ELSE NULL END,
 next_at=$8::timestamptz + make_interval(secs=>least(30,power(2,least(attempt_count-1,5)))::double precision)
 WHERE id=$1 AND run_id=$2 AND kind=$3 AND subject=$4 AND attempt_count=$5 AND worker_epoch=$6
 AND claimed AND completed_at IS NULL AND (next_at>$8 OR $7<>'')`, op.ID, op.RunID, string(op.Kind), op.Subject, op.Attempt, lease.Epoch, string(debt), now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrRunCancellationProgressStale
	}
	return nil
}

// No caller holds the worker row and then waits for a Run. Progress takes the
// Run first and the worker row last; ownership-only operations take no Run lock.
func lockCancellationProgress(ctx context.Context, tx Tx, lease RunCancellationLease, runID int) (time.Time, time.Time, error) {
	if err := lockCancellingRun(ctx, tx, runID); err != nil {
		return time.Time{}, time.Time{}, err
	}
	return lockCurrentCancellationLease(ctx, tx, lease)
}

func lockCancellingRun(ctx context.Context, tx Tx, runID int) error {
	return lockRunCancellationState(ctx, tx, runID, false)
}

func lockRunCancellationState(ctx context.Context, tx Tx, runID int, terminalReplay bool) error {
	var version, status string
	var requested bool
	if err := tx.QueryRowContext(ctx, `SELECT run_contract_version,status,cancel_requested_at IS NOT NULL FROM pipeline_runs WHERE id=$1 FOR NO KEY UPDATE`, runID).Scan(&version, &status, &requested); err != nil {
		return err
	}
	if version != string(atc.RunContractV2) || !requested || (status != string(atc.RunStatusRunning) && !(terminalReplay && status == string(atc.RunStatusAborted))) {
		return ErrPipelineRunNotRunning
	}
	return nil
}

func lockCurrentCancellationLease(ctx context.Context, tx Tx, lease RunCancellationLease) (time.Time, time.Time, error) {
	if err := lockCancellationWorker(ctx, tx); err != nil {
		return time.Time{}, time.Time{}, err
	}
	var now, expires time.Time
	err := tx.QueryRowContext(ctx, `WITH lease_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
 SELECT t.now,w.expires_at FROM pipeline_run_cancellation_worker w,lease_time t
 WHERE w.singleton AND w.owner_id=$1 AND w.worker_epoch=$2 AND w.expires_at>t.now`, lease.OwnerID, lease.Epoch).Scan(&now, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrRunCancellationLeaseLost
	}
	return now.UTC(), expires.UTC(), err
}
