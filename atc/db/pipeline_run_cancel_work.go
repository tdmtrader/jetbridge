package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// RunCancellationLease identifies one restart-safe cleanup worker. These times
// come from the database clock; Epoch advances whenever ownership is reacquired
// after expiry, including by the same process identity.
type RunCancellationLease struct {
	OwnerID     string
	Epoch       int64
	DatabaseNow time.Time
	ExpiresAt   time.Time
}

const RunCancellationLeaseTerm = time.Minute

var (
	ErrRunCancellationLeaseLost    = errors.New("cancellation worker lease expired or changed owner")
	ErrRunCancellationLeaseInvalid = errors.New("invalid cancellation worker lease")
)

// ClaimRunCancellationLease owns no Run/domain locks and makes no external call.
// A live owner's retry preserves its epoch and renews its term, which is how a
// worker renews: the term outlasts a pass. Reacquisition after expiry always
// advances it, so an old request from that same process cannot become current.
func (f *pipelineRunFactory) ClaimRunCancellationLease(ctx context.Context, tx Tx, owner string, term time.Duration) (RunCancellationLease, bool, error) {
	interval, err := cancellationLeaseInterval(owner, term)
	if err != nil {
		return RunCancellationLease{}, false, err
	}
	// Establish the singleton, then lock it before sampling the clock. A statement
	// timestamp sampled before waiting for another transaction could renew a lease
	// that expired while blocked, or fail to fence an expired same-owner claim.
	if _, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_cancellation_worker
 (singleton,owner_id,worker_epoch,renewed_at,expires_at)
 VALUES(true,$1,1,clock_timestamp(),clock_timestamp()+$2::interval)
 ON CONFLICT(singleton) DO NOTHING`, owner, interval); err != nil {
		return RunCancellationLease{}, false, err
	}
	if err := lockCancellationWorker(ctx, tx); err != nil {
		return RunCancellationLease{}, false, err
	}
	var lease RunCancellationLease
	err = tx.QueryRowContext(ctx, `WITH lease_time AS MATERIALIZED (SELECT clock_timestamp() AS now)
 UPDATE pipeline_run_cancellation_worker SET
  owner_id=$1,
  worker_epoch=pipeline_run_cancellation_worker.worker_epoch + CASE
   WHEN owner_id=$1 AND expires_at>t.now THEN 0 ELSE 1 END,
  renewed_at=t.now, expires_at=t.now+$2::interval
 FROM lease_time t WHERE singleton AND (owner_id=$1 OR expires_at<=t.now)
 RETURNING owner_id,worker_epoch,renewed_at,expires_at`, owner, interval).
		Scan(&lease.OwnerID, &lease.Epoch, &lease.DatabaseNow, &lease.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RunCancellationLease{}, false, nil
	}
	lease.DatabaseNow, lease.ExpiresAt = lease.DatabaseNow.UTC(), lease.ExpiresAt.UTC()
	return lease, err == nil, err
}

func lockCancellationWorker(ctx context.Context, tx Tx) error {
	var epoch int64
	err := tx.QueryRowContext(ctx, `SELECT worker_epoch FROM pipeline_run_cancellation_worker WHERE singleton FOR UPDATE`).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrRunCancellationLeaseLost
	}
	return err
}

func cancellationLeaseInterval(owner string, term time.Duration) (string, error) {
	if strings.TrimSpace(owner) == "" || len(owner) > 128 || term.Microseconds() < 1 || term > RunCancellationLeaseTerm {
		return "", ErrRunCancellationLeaseInvalid
	}
	return fmt.Sprintf("%d microseconds", term.Microseconds()), nil
}
