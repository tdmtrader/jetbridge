package steps

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type cancellationQueueStore interface {
	cancellationLeaseStore
	PendingRunCancellations(context.Context, db.Tx, db.RunCancellationLease, int) ([]int, error)
	DiscoverRunCancellation(context.Context, db.Tx, db.RunCancellationLease, int, int) (int, error)
	ClaimRunCancellationOperation(context.Context, db.Tx, db.RunCancellationLease, int) (db.RunCancellationOperation, bool, error)
	RecordRunCancellationProgress(context.Context, db.Tx, db.RunCancellationLease, db.RunCancellationOperation, db.RunCancellationDebt) error
}

func CancellationQueueDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, CancellationLeaseResult]("cancellation cleanup bookkeeping exercises {string}", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			mode, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseCancellationQueue(in, mode)}, nil
		}),
		CheckThat[CancellationLeaseResult]("cleanup progress is bounded and survives replacement", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

// These scenarios exercise the queue through the actual factory and PostgreSQL.
// Recording an operation result tests bookkeeping only; it does not assert that
// a process was stopped or that the Run can publish an aborted result.
func exerciseCancellationQueue(in RunOutputStart, mode string) error {
	store, ok := any(db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)).(cancellationQueueStore)
	if !ok {
		return fmt.Errorf("Run factory has no durable typed cancellation work queue")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transaction := func(rollback bool, f func(db.Tx) error) error {
		tx, err := in.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer db.Rollback(tx)
		if err := f(tx); err != nil {
			return err
		}
		if rollback {
			return nil
		}
		return tx.Commit()
	}
	var lease db.RunCancellationLease
	claimLease := func(owner string) error {
		return transaction(false, func(tx db.Tx) error {
			var found bool
			var err error
			lease, found, err = store.ClaimRunCancellationLease(ctx, tx, owner, time.Minute)
			if err == nil && !found {
				err = fmt.Errorf("worker lease not acquired")
			}
			return err
		})
	}
	if err := claimLease("queue-worker"); err != nil {
		return err
	}
	runID := in.Creation.Run.ID()
	if mode == "ordinary Run excluded" {
		return transaction(false, func(tx db.Tx) error {
			ids, err := store.PendingRunCancellations(ctx, tx, lease, 50)
			if err == nil && len(ids) != 0 {
				err = fmt.Errorf("ordinary Run was selected for cancellation")
			}
			return err
		})
	}
	if mode == "run rotation and bound" {
		team, found, err := in.DB.TeamFactory.FindTeam("output-start")
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("missing fixture team")
		}
		template, found, err := team.Pipeline(atc.PipelineRef{Name: "review"})
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("missing fixture template")
		}
		factory := db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)
		for i := 0; i < 51; i++ {
			if err := transaction(false, func(tx db.Tx) error {
				creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "queue-fixture", db.RunCreationOpts{ActivationEpoch: int64(hangarEpoch)})
				if err != nil {
					return err
				}
				_, err = factory.AcceptRunCancellation(ctx, tx, creation.Run.ID(), "owner", nil)
				return err
			}); err != nil {
				return err
			}
		}
		seen := map[int]bool{}
		for i := 0; i < 2; i++ {
			if err := transaction(false, func(tx db.Tx) error {
				ids, err := store.PendingRunCancellations(ctx, tx, lease, 50)
				if err != nil {
					return err
				}
				if len(ids) == 0 || len(ids) > 50 {
					return fmt.Errorf("unbounded or empty Run page: %d", len(ids))
				}
				for _, id := range ids {
					if id == runID {
						return fmt.Errorf("uncancelled Run selected")
					}
					seen[id] = true
				}
				return nil
			}); err != nil {
				return err
			}
			store = any(db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)).(cancellationQueueStore)
		}
		if len(seen) != 51 {
			return fmt.Errorf("restart failed to advance past first Run page: %d", len(seen))
		}
		return nil
	}
	if mode == "poison debt and restart" || mode == "discovery bound" || mode == "finite high-water" {
		job, err := cancellationJob(in)
		if err != nil {
			return err
		}
		for i := 0; i < 120; i++ {
			if _, err := job.CreateBuild("queue-fixture"); err != nil {
				return err
			}
		}
	}
	if _, err := in.start(in.Plan, int64(hangarEpoch), "brine-node", hangarNodeUID, false); err != nil {
		return err
	}
	if _, err := acceptRunCancellation(in, "owner", nil, false); err != nil {
		return err
	}
	discover := func(limit int) (int, error) {
		var count int
		err := transaction(false, func(tx db.Tx) error {
			var err error
			count, err = store.DiscoverRunCancellation(ctx, tx, lease, runID, limit)
			return err
		})
		return count, err
	}
	claim := func(rollback bool) (db.RunCancellationOperation, bool, error) {
		var op db.RunCancellationOperation
		var found bool
		err := transaction(rollback, func(tx db.Tx) error {
			var err error
			op, found, err = store.ClaimRunCancellationOperation(ctx, tx, lease, runID)
			return err
		})
		return op, found, err
	}
	record := func(op db.RunCancellationOperation, debt db.RunCancellationDebt) error {
		return transaction(false, func(tx db.Tx) error { return store.RecordRunCancellationProgress(ctx, tx, lease, op, debt) })
	}
	if mode == "invalid limits" {
		for _, limit := range []int{-1, 0, 101} {
			if _, err := discover(limit); err == nil {
				return fmt.Errorf("invalid discovery limit accepted: %d", limit)
			}
		}
		for _, limit := range []int{-1, 0, 51} {
			if err := transaction(false, func(tx db.Tx) error { _, err := store.PendingRunCancellations(ctx, tx, lease, limit); return err }); err == nil {
				return fmt.Errorf("invalid Run limit accepted: %d", limit)
			}
		}
		return nil
	}
	limit := 100
	if mode == "discovery bound" || mode == "finite high-water" {
		limit = 3
	}
	count, err := discover(limit)
	if err != nil {
		return err
	}
	if count < 1 || count > limit {
		return fmt.Errorf("discovery not bounded: %d", count)
	}
	if mode == "discovery bound" {
		return nil
	}
	if mode == "exact discovery and replay" {
		var total int
		if err := in.DB.Conn.QueryRow(`SELECT count(*) FROM pipeline_run_cancellation_operations WHERE run_id=$1`, runID).Scan(&total); err != nil {
			return err
		}
		count, err = discover(100)
		if err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("replayed discovery duplicated %d operations", count)
		}
		for _, build := range in.Creation.EntryBuilds {
			var found bool
			if err := in.DB.Conn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind='build_abort' AND subject=$2)`, runID, strconv.Itoa(build.ID())).Scan(&found); err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("owned build %d not discovered", build.ID())
			}
		}
		if total < 6 {
			return fmt.Errorf("discovery omitted known kinds: %d", total)
		}
		return nil
	}
	if mode == "safe residual deadline" {
		if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_worker SET expires_at=clock_timestamp()+interval '4 seconds'`); err != nil {
			return err
		}
	}
	op, found, err := claim(mode == "claim rollback")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("no operation claimed")
	}
	if mode == "safe residual deadline" {
		var expires time.Time
		if err := in.DB.Conn.QueryRow(`SELECT expires_at FROM pipeline_run_cancellation_worker WHERE singleton`).Scan(&expires); err != nil {
			return err
		}
		if expires.Sub(op.Deadline) != time.Second || !op.Deadline.After(op.DatabaseNow) {
			return fmt.Errorf("operation ignored the current lease's safe residual term")
		}
		return nil
	}
	if mode == "unknown debt" {
		if err := record(op, db.RunCancellationDebt("untyped-work")); err == nil {
			return fmt.Errorf("unknown retry debt accepted")
		}
		return record(op, db.CancellationDone)
	}
	if mode == "expired lease progress" {
		if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second'`); err != nil {
			return err
		}
		if err := record(op, db.CancellationDone); err == nil {
			return fmt.Errorf("expired owner recorded progress")
		}
		return nil
	}
	if mode == "expired operation response" || mode == "interrupted claim recovery" || mode == "timeout debt" {
		if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1`, op.ID); err != nil {
			return err
		}
		if mode == "expired operation response" {
			if err := record(op, db.CancellationDone); err == nil {
				return fmt.Errorf("late operation response overwrote retryable debt")
			}
			return nil
		}
		if mode == "timeout debt" {
			if err := record(op, db.CancellationTimeout); err != nil {
				return err
			}
			var future bool
			var debt string
			if err := in.DB.Conn.QueryRow(`SELECT next_at>clock_timestamp(),debt FROM pipeline_run_cancellation_operations WHERE id=$1`, op.ID).Scan(&future, &debt); err != nil {
				return err
			}
			if !future || debt != "timeout" {
				return fmt.Errorf("timed-out operation lost its retry backoff")
			}
			return nil
		}
		store = any(db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)).(cancellationQueueStore)
		for i := 0; i < 20; i++ {
			next, found, err := claim(false)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			if next.ID == op.ID {
				if next.Attempt != 2 {
					return fmt.Errorf("interrupted claim lost its exact attempt identity")
				}
				if err := record(op, db.CancellationDone); err == nil {
					return fmt.Errorf("previous attempt overwrote its replacement")
				}
				return record(next, db.CancellationDone)
			}
			if err := record(next, db.CancellationDone); err != nil {
				return err
			}
		}
		return fmt.Errorf("interrupted claim never became retryable")
	}
	if mode == "progress rollback" {
		if err := transaction(true, func(tx db.Tx) error {
			return store.RecordRunCancellationProgress(ctx, tx, lease, op, db.CancellationDone)
		}); err != nil {
			return err
		}
		return record(op, db.CancellationPending)
	}
	if mode == "claim rollback" {
		next, found, err := claim(false)
		if err != nil {
			return err
		}
		if !found || op.ID != next.ID || next.Attempt != 1 {
			return fmt.Errorf("rolled-back claim advanced progress")
		}
		return nil
	}
	if mode == "claim deadline" {
		if op.Attempt != 1 || op.WorkerEpoch != lease.Epoch || !op.Deadline.After(op.DatabaseNow) || op.Deadline.Sub(op.DatabaseNow) > db.RunCancellationOperationTerm || !op.Deadline.Before(lease.ExpiresAt) {
			return fmt.Errorf("operation lacks bounded DB-clock deadline and fenced attempt")
		}
		return nil
	}
	if mode == "stale epoch" {
		if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second'`); err != nil {
			return err
		}
		if err := claimLease("replacement-worker"); err != nil {
			return err
		}
		if err := record(op, db.CancellationDone); err == nil {
			return fmt.Errorf("new owner accepted a superseded operation")
		}
		return nil
	}
	if mode == "stale attempt" {
		if err := record(op, db.CancellationPending); err != nil {
			return err
		}
		if err := record(op, db.CancellationDone); err == nil {
			return fmt.Errorf("late response overwrote recorded retry debt")
		}
		return nil
	}
	if mode == "finite high-water" {
		// Discover more real pre-cancel builds after the current cycle is fixed.
		if _, err := discover(100); err != nil {
			return err
		}
		seen := map[int64]bool{op.ID: true}
		if err := record(op, db.CancellationDone); err != nil {
			return err
		}
		for i := 0; i < 150; i++ {
			next, found, err := claim(false)
			if err != nil {
				return err
			}
			if !found {
				break
			}
			seen[next.ID] = true
			if err := record(next, db.CancellationDone); err != nil {
				return err
			}
		}
		if len(seen) < 100 {
			return fmt.Errorf("new work never entered a later finite cycle: %d", len(seen))
		}
		return nil
	}
	if err := record(op, db.CancellationUnavailable); err != nil {
		return err
	}
	var attempts int
	var debt string
	var delay float64
	if err := in.DB.Conn.QueryRow(`SELECT attempt_count,debt,extract(epoch FROM next_at-clock_timestamp()) FROM pipeline_run_cancellation_operations WHERE id=$1`, op.ID).Scan(&attempts, &debt, &delay); err != nil {
		return err
	}
	if attempts != 1 || debt != "unavailable" || delay <= 0 || delay > 30 {
		return fmt.Errorf("failed operation has no bounded durable retry debt")
	}
	store = any(db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)).(cancellationQueueStore)
	seen := map[int64]bool{}
	kinds := map[db.RunCancellationKind]bool{}
	poisonAttempt := op.Attempt
	for i := 0; i < 100; i++ {
		next, found, err := claim(false)
		if err != nil {
			return err
		}
		if !found {
			break
		}
		if next.ID == op.ID {
			if len(seen) == 0 {
				return fmt.Errorf("poison operation pinned the first position")
			}
			poisonAttempt = next.Attempt
			if err := record(next, db.CancellationUnavailable); err != nil {
				return err
			}
			continue
		}
		seen[next.ID] = true
		kinds[next.Kind] = true
		if err := record(next, db.CancellationDone); err != nil {
			return err
		}
	}
	if len(kinds) < 3 {
		return fmt.Errorf("operation kinds starved: %v", kinds)
	}
	if mode == "poison debt and restart" && len(seen) < 90 {
		return fmt.Errorf("later identities starved after restart: %d", len(seen))
	}
	// Make only the failed debt due using the same database clock, then prove it
	// re-enters a fair cycle. No sleep or fake clock determines the winner.
	if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		return err
	}
	for i := 0; i < 10; i++ {
		next, found, err := claim(false)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		if next.ID == op.ID {
			if next.Attempt != poisonAttempt+1 {
				return fmt.Errorf("retry lost its attempt count")
			}
			return nil
		}
		if err := record(next, db.CancellationDone); err != nil {
			return err
		}
	}
	return fmt.Errorf("due poison debt was lost after advancing the cursor")
}
