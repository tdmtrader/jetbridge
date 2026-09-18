package steps

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// runReaper separates fixture failures from the production result. A failed
// concurrency barrier must never count as the expected reaper error.
func runReaper(in ReaperReady) (out ReaperOutcome, fixtureErr error) {
	out.Ready = in
	if in.RacePod == nil {
		out.Err = in.Reaper.Run(in.Ctx)
		return out, nil
	}

	ctx, cancel := context.WithTimeout(in.Ctx, 15*time.Second)
	defer cancel()
	// postgresrunner deliberately limits each pool to one connection. The
	// competing actor needs its own pool, or the sweep would wait for a free
	// client connection rather than reach PostgreSQL's lock.
	competitor := in.DB.runner.OpenConn()
	defer competitor.Close()
	tx, err := competitor.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "LOCK TABLE containers IN ACCESS EXCLUSIVE MODE"); err != nil {
		return out, fmt.Errorf("hold sweep at container reporting: %w", err)
	}
	var blockerPID int
	if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID); err != nil {
		return out, err
	}

	done := make(chan error, 1)
	go func() { done <- in.Reaper.Run(ctx) }()
	finished := false
	defer func() {
		cancel()
		_ = tx.Rollback() // always unblock the production goroutine first
		if !finished {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				fixtureErr = errors.Join(fixtureErr, fmt.Errorf("reaper did not stop after releasing the race barrier"))
			}
		}
	}()

	// The reaper has listed pods before reaching the containers table. Wait
	// for PostgreSQL to confirm it is blocked by OUR transaction, not for an
	// assumed amount of time. pg_locks is live even inside this transaction.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_locks
			WHERE NOT granted AND locktype = 'relation'
			  AND relation = 'containers'::regclass
			  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
			  AND $1 = ANY(pg_blocking_pids(pid))
		)`, blockerPID).Scan(&blocked); err != nil {
			return out, fmt.Errorf("observe sweep barrier: %w", err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			finished = true
			return out, fmt.Errorf("sweep finished before concurrent deletion: %v", err)
		case <-ctx.Done():
			return out, fmt.Errorf("sweep never reached the deletion barrier: %w", ctx.Err())
		case <-tick.C:
		}
	}

	pods := in.Clientset.CoreV1().Pods(in.Config.Namespace)
	zero := int64(0)
	if err := pods.Delete(ctx, in.RacePod.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &zero,
		Preconditions:      &metav1.Preconditions{UID: &in.RacePod.UID},
	}); err != nil {
		return out, fmt.Errorf("concurrent pod deletion: %w", err)
	}
	if _, err := pods.Get(ctx, in.RacePod.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return out, fmt.Errorf("concurrent deletion did not remove the pod: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		return out, fmt.Errorf("release sweep barrier: %w", err)
	}
	select {
	case out.Err = <-done:
		finished = true
		return out, nil
	case <-ctx.Done():
		return out, fmt.Errorf("sweep did not finish after concurrent deletion: %w", ctx.Err())
	}
}
