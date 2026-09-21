package steps

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
)

type cancellationLeaseStore interface {
	ClaimRunCancellationLease(context.Context, db.Tx, string, time.Duration) (db.RunCancellationLease, bool, error)
	RenewRunCancellationLease(context.Context, db.Tx, db.RunCancellationLease, time.Duration) (db.RunCancellationLease, error)
}

type CancellationLeaseResult struct{ Err error }

func CancellationLeaseDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[RunOutputStart, CancellationLeaseResult]("cancellation ownership exercises {string}", func(in RunOutputStart, p brine.Params, _ *brine.Recorder) (CancellationLeaseResult, error) {
			mode, _ := p.GetString(0)
			return CancellationLeaseResult{Err: exerciseCancellationLease(in, mode)}, nil
		}),
		CheckThat[CancellationLeaseResult]("the cancellation lease preserves exclusive database-clock ownership", func(in CancellationLeaseResult) error { return in.Err }),
	}
}

func exerciseCancellationLease(in RunOutputStart, mode string) error {
	store, ok := any(db.NewPipelineRunFactory(in.DB.Conn, in.DB.LockFactory)).(cancellationLeaseStore)
	if !ok {
		return fmt.Errorf("Run factory has no restart-safe cancellation lease")
	}
	ctx := context.Background()
	claim := func(owner string, rollback bool) (db.RunCancellationLease, bool, error) {
		tx, err := in.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return db.RunCancellationLease{}, false, err
		}
		defer db.Rollback(tx)
		lease, found, err := store.ClaimRunCancellationLease(ctx, tx, owner, time.Minute)
		if err == nil && !rollback {
			err = tx.Commit()
		}
		return lease, found, err
	}
	renew := func(lease db.RunCancellationLease) (db.RunCancellationLease, error) {
		tx, err := in.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			return db.RunCancellationLease{}, err
		}
		defer db.Rollback(tx)
		result, err := store.RenewRunCancellationLease(ctx, tx, lease, time.Minute)
		if err == nil {
			err = tx.Commit()
		}
		return result, err
	}
	if mode == "racing owners" {
		in.DB.Conn.SetMaxOpenConns(4)
		var wg sync.WaitGroup
		var granted [2]bool
		var errs [2]error
		start := make(chan struct{})
		for i := range granted {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				_, granted[i], errs[i] = claim(fmt.Sprintf("worker-%d", i), false)
			}(i)
		}
		close(start)
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return err
			}
		}
		if granted[0] == granted[1] {
			return fmt.Errorf("concurrent claims did not select exactly one owner")
		}
		return nil
	}
	lease, found, err := claim("first-worker", mode == "rollback")
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("first cancellation worker was not admitted")
	}
	var now time.Time
	if err := in.DB.Conn.QueryRow(`SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	if lease.OwnerID != "first-worker" || lease.Epoch < 1 || lease.DatabaseNow.After(now) || lease.ExpiresAt.Sub(lease.DatabaseNow) != time.Minute {
		return fmt.Errorf("lease does not carry its server-derived owner, epoch and bounded DB term")
	}
	switch mode {
	case "renewal blocked past expiry", "claim blocked past expiry":
		return exerciseDelayedCancellationLease(in, store, lease, mode == "claim blocked past expiry")
	case "first claim":
		return nil
	case "competing owner":
		_, found, err := claim("second-worker", false)
		if err != nil {
			return err
		}
		if found {
			return fmt.Errorf("second worker stole an unexpired lease")
		}
		return nil
	case "renewal":
		next, err := renew(lease)
		if err != nil {
			return err
		}
		if next.OwnerID != lease.OwnerID || next.Epoch != lease.Epoch || next.DatabaseNow.Before(lease.DatabaseNow) || next.ExpiresAt.Before(lease.ExpiresAt) {
			return fmt.Errorf("renewal changed ownership or moved the DB deadline backwards")
		}
		return nil
	case "rollback":
		_, found, err := claim("second-worker", false)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("rolled-back lease fenced the next worker")
		}
		return nil
	case "takeover", "same owner after expiry", "expired renewal":
		if _, err := in.DB.Conn.Exec(`UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second'`); err != nil {
			return err
		}
		if mode == "expired renewal" {
			if _, err := renew(lease); err == nil {
				return fmt.Errorf("expired lease was resurrected by renewal")
			}
			return nil
		}
		owner := "second-worker"
		if mode == "same owner after expiry" {
			owner = lease.OwnerID
		}
		next, found, err := claim(owner, false)
		if err != nil {
			return err
		}
		if !found || next.Epoch <= lease.Epoch || next.OwnerID != owner {
			return fmt.Errorf("takeover did not advance the persisted worker epoch")
		}
		if _, err := renew(lease); err == nil {
			return fmt.Errorf("superseded worker renewed after takeover")
		}
		return nil
	default:
		return fmt.Errorf("unknown lease case %q", mode)
	}
}

// A real row lock holds the request across expiry. The test observes PostgreSQL's
// lock wait before advancing the stored deadline, so no wall-clock sleep decides
// which side won. A clock sampled before taking the lock would revive the lease.
func exerciseDelayedCancellationLease(in RunOutputStart, store cancellationLeaseStore, lease db.RunCancellationLease, claim bool) error {
	in.DB.Conn.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	blocker, err := in.DB.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(blocker)
	if _, err := blocker.ExecContext(ctx, `UPDATE pipeline_run_cancellation_worker SET owner_id=owner_id`); err != nil {
		return err
	}
	type result struct {
		Lease db.RunCancellationLease
		Found bool
		Err   error
	}
	done := make(chan result, 1)
	started := make(chan int, 1)
	go func() {
		tx, err := in.DB.Conn.BeginTx(ctx, nil)
		if err != nil {
			done <- result{Err: err}
			return
		}
		defer db.Rollback(tx)
		var pid int
		if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			done <- result{Err: err}
			return
		}
		started <- pid
		var out result
		if claim {
			out.Lease, out.Found, out.Err = store.ClaimRunCancellationLease(ctx, tx, lease.OwnerID, time.Minute)
		} else {
			out.Lease, out.Err = store.RenewRunCancellationLease(ctx, tx, lease, time.Minute)
		}
		if out.Err == nil {
			out.Err = tx.Commit()
		}
		done <- out
	}()
	var pid int
	select {
	case pid = <-started:
	case out := <-done:
		return out.Err
	case <-ctx.Done():
		return ctx.Err()
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err := in.DB.Conn.QueryRowContext(ctx, `SELECT coalesce(wait_event_type='Lock',false) FROM pg_stat_activity WHERE pid=$1`, pid).Scan(&waiting); err != nil {
			return err
		}
		if waiting {
			break
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			return fmt.Errorf("lease operation never waited for its DB row: %w", ctx.Err())
		}
	}
	if _, err := blocker.ExecContext(ctx, `UPDATE pipeline_run_cancellation_worker SET renewed_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 microsecond'`); err != nil {
		return err
	}
	if err := blocker.Commit(); err != nil {
		return err
	}
	select {
	case out := <-done:
		if !claim {
			if !errors.Is(out.Err, db.ErrRunCancellationLeaseLost) {
				return fmt.Errorf("blocked renewal resurrected expired ownership: %v", out.Err)
			}
			return nil
		}
		if out.Err != nil {
			return out.Err
		}
		if !out.Found || out.Lease.Epoch <= lease.Epoch {
			return fmt.Errorf("blocked same-owner claim retained its expired epoch")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
