package db

import (
	"context"
	"database/sql"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

// The landing queue's gates (ADR-0011). Every method that follows a gate
// Run's outcome takes the engine's transaction and changes the row only while
// it still names that Run with a pending verdict, so a pass repeated after a
// restart, or racing another, changes nothing a second time. The engine locks
// the candidate's intent (OpenIntents) before it calls any of them: intents,
// then gates, then entries, then the queue.

var landingGatesQuery = psql.Select("g.id", "g.compose_run_id", "g.ordinal", "g.template", "g.wait_seconds", "g.run_id",
	"g.attempts", "g.no_verdicts", "g.verdict", "g.last_error",
	"(g.run_id IS NOT NULL AND g.admitted_at + make_interval(secs => g.wait_seconds) < now())").
	From("landing_gates g")

func scanLandingGate(row scannable) (LandingGateRow, error) {
	var gate LandingGateRow
	var waitSeconds int
	var run sql.NullInt64
	err := row.Scan(&gate.ID, &gate.ComposeRunID, &gate.Ordinal, &gate.Template, &waitSeconds, &run,
		&gate.Attempts, &gate.NoVerdicts, &gate.Verdict, &gate.LastError, &gate.Overdue)
	if err != nil {
		return LandingGateRow{}, err
	}
	gate.Wait = time.Duration(waitSeconds) * time.Second
	gate.RunID = nullableInt(run)
	return gate, nil
}

// Gates is every gate of the candidate its compose Run names, in the queue's
// order.
func (f *landingQueueFactory) Gates(ctx context.Context, composeRunID int) ([]LandingGateRow, error) {
	rows, err := landingGatesQuery.Where(sq.Eq{"g.compose_run_id": composeRunID}).OrderBy("g.ordinal").
		RunWith(f.conn).QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var gates []LandingGateRow
	for rows.Next() {
		gate, err := scanLandingGate(rows)
		if err != nil {
			return nil, err
		}
		gates = append(gates, gate)
	}
	return gates, rows.Err()
}

// RecordGates records the gate Runs admitted for a composed candidate and
// takes its intent from composing to gating, in the transaction that
// admitted them. False means the intent was no longer composing: the caller
// rolls back, admissions and all.
func (f *landingQueueFactory) RecordGates(ctx context.Context, tx Tx, intent LandingIntent, gates []LandingGateAdmission) (bool, error) {
	result, err := psql.Update("landing_intents").
		Set("state", LandingIntentGating).Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": intent.ID, "state": LandingIntentComposing}).
		RunWith(tx).ExecContext(ctx)
	if err != nil {
		return false, err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	insert := psql.Insert("landing_gates").Columns("compose_run_id", "ordinal", "template", "wait_seconds", "run_id", "admitted_at", "attempts")
	for _, gate := range gates {
		insert = insert.Values(intent.ComposeRunID, gate.Ordinal, gate.Template, waitSeconds(gate.Wait), gate.RunID, sq.Expr("now()"), 1)
	}
	if _, err := insert.RunWith(tx).ExecContext(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RecordGateRun names the next Run of a gate that was due one.
func (f *landingQueueFactory) RecordGateRun(ctx context.Context, tx Tx, gate LandingGateRow, runID int) (bool, error) {
	return affectedOne(psql.Update("landing_gates").
		Set("run_id", runID).Set("admitted_at", sq.Expr("now()")).Set("attempts", sq.Expr("attempts + 1")).Set("updated_at", sq.Expr("now()")).
		Where(sq.And{sq.Eq{"id": gate.ID, "verdict": atc.LandingGatePending}, sq.Expr("run_id IS NULL")}).
		RunWith(tx).ExecContext(ctx))
}

// PassGate records the gate's Run as succeeded.
func (f *landingQueueFactory) PassGate(ctx context.Context, tx Tx, gate LandingGateRow) (bool, error) {
	return f.settleGate(ctx, tx, gate, psql.Update("landing_gates").Set("verdict", atc.LandingGatePassed).Set("last_error", ""))
}

// FailGate records the gate's red verdict. The caller ejects the entry in the
// same transaction.
func (f *landingQueueFactory) FailGate(ctx context.Context, tx Tx, gate LandingGateRow, reason string) (bool, error) {
	return f.settleGate(ctx, tx, gate, psql.Update("landing_gates").Set("verdict", atc.LandingGateFailed).Set("last_error", reason))
}

// RetryGate records a Run that gave no verdict and makes the gate due another.
func (f *landingQueueFactory) RetryGate(ctx context.Context, tx Tx, gate LandingGateRow, reason string) (bool, error) {
	return f.settleGate(ctx, tx, gate, psql.Update("landing_gates").
		Set("run_id", nil).Set("no_verdicts", sq.Expr("no_verdicts + 1")).Set("last_error", reason))
}

// HoldGate records a Run that gave no verdict when its gate has no retry
// left, and pauses the queue naming it.
func (f *landingQueueFactory) HoldGate(ctx context.Context, tx Tx, intent LandingIntent, gate LandingGateRow, reason string) (bool, error) {
	held, err := f.settleGate(ctx, tx, gate, psql.Update("landing_gates").
		Set("verdict", atc.LandingGateNoVerdict).Set("no_verdicts", sq.Expr("no_verdicts + 1")).Set("last_error", reason))
	if err != nil || !held {
		return held, err
	}
	_, err = psql.Update("landing_queues").Set("paused", true).Set("pause_reason", reason).Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": intent.QueueID}).RunWith(tx).ExecContext(ctx)
	return err == nil, err
}

func (f *landingQueueFactory) settleGate(ctx context.Context, tx Tx, gate LandingGateRow, update sq.UpdateBuilder) (bool, error) {
	if gate.RunID == nil {
		return false, nil
	}
	return affectedOne(update.Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": gate.ID, "run_id": *gate.RunID, "verdict": atc.LandingGatePending}).
		RunWith(tx).ExecContext(ctx))
}

// Resume clears the queue's pause and makes every gate it was held on due a
// fresh Run with its retry restored. False means the queue was not paused.
func (f *landingQueueFactory) Resume(ctx context.Context, queueID int) (bool, error) {
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	// The engine's order: the queue's open intents first, then their gates,
	// then the queue.
	if _, err := f.OpenIntents(ctx, tx, queueID); err != nil {
		return false, err
	}
	_, err = psql.Update("landing_gates").
		Set("verdict", atc.LandingGatePending).Set("run_id", nil).Set("no_verdicts", 0).Set("updated_at", sq.Expr("now()")).
		Where(sq.And{
			sq.Eq{"verdict": atc.LandingGateNoVerdict},
			sq.Expr("compose_run_id IN (SELECT compose_run_id FROM landing_intents WHERE queue_id = ? AND state <> ?)", queueID, LandingIntentDone),
		}).RunWith(tx).ExecContext(ctx)
	if err != nil {
		return false, err
	}
	resumed, err := affectedOne(psql.Update("landing_queues").Set("paused", false).Set("pause_reason", "").Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": queueID, "paused": true}).RunWith(tx).ExecContext(ctx))
	if err != nil || !resumed {
		// Not paused: roll back, so nothing else changed either.
		return false, err
	}
	return true, tx.Commit()
}

// entryGates is the gates of each entry's latest candidate, by entry id, for
// the queue's status.
func (f *landingQueueFactory) entryGates(ctx context.Context, queueID int) (map[string][]atc.LandingGate, error) {
	rows, err := psql.Select("e.entry_id", "g.template", "COALESCE(r.number, 0)", "g.verdict", "g.attempts", "g.last_error").
		From("landing_gates g").
		Join("landing_entries e ON e.compose_run_id = g.compose_run_id").
		LeftJoin("pipeline_runs r ON r.id = g.run_id").
		Where(sq.Eq{"e.queue_id": queueID}).OrderBy("e.id", "g.ordinal").
		RunWith(f.conn).QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	gates := map[string][]atc.LandingGate{}
	for rows.Next() {
		var entryID string
		var gate atc.LandingGate
		if err := rows.Scan(&entryID, &gate.Template, &gate.Run, &gate.Verdict, &gate.Attempts, &gate.LastError); err != nil {
			return nil, err
		}
		gates[entryID] = append(gates[entryID], gate)
	}
	return gates, rows.Err()
}

func affectedOne(result sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

func waitSeconds(wait time.Duration) int {
	seconds := int(wait / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
