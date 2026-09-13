package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/concourse/concourse/hangar/output"
)

// The work every Hangar output repository method does the same way, in one
// place: reading a row over the two methods output.Tx has, reading the database
// clock, turning a lease term into something SQL will accept, mapping a
// PostgreSQL failure onto the leaf's typed outcomes, and waking a worker after
// a commit.
//
// Each was written three or four times before it lived here, and each of the
// four is somewhere a second spelling would be a second behaviour: a clock read
// that fell back to the process clock, a lease term rounded differently, a
// conflict that mapped to the wrong sentinel, or a notification sent before the
// commit it announces.

// hangarQueryRow is QueryRow over the two methods output.Tx has.
//
// The leaf's Tx is deliberately ExecContext and QueryContext and nothing that
// can commit, so there is no QueryRowContext to call. Absence is
// output.ErrNotFound rather than sql.ErrNoRows, because a caller of this
// package checks the leaf's sentinels and should not have to check two sets.
func hangarQueryRow(ctx context.Context, tx output.Tx, query string, args []any, into ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return hangarConflict(err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return hangarConflict(err)
		}

		return fmt.Errorf("%w: no row", output.ErrNotFound)
	}
	if err := rows.Scan(into...); err != nil {
		return err
	}

	return rows.Err()
}

// hangarDatabaseNow reads the clock every deadline in this plane is measured
// against.
//
// Requirements 10, 11, 36, 39 and 48 all say "database clock", and they mean
// it: a node whose clock drifts must not be able to expire its own hold, and
// expiry alone is never proof or release authority. There is deliberately no
// fallback to time.Now() -- a fallback would be exactly the drift the rule
// exists to exclude, quietly.
func hangarDatabaseNow(ctx context.Context, tx output.Tx) (time.Time, error) {
	var now time.Time
	if err := hangarQueryRow(ctx, tx, `SELECT now()`, nil, &now); err != nil {
		return time.Time{}, err
	}

	return now, nil
}

// hangarLeaseInterval renders a lease term for `now() + $n::interval`.
//
// Whole seconds, once, here: a term rendered two ways in two call sites is two
// terms, and the floor checks that guard them would then be checking different
// things from what the database stores.
func hangarLeaseInterval(term time.Duration) (string, error) {
	if term < output.MinLeaseTerm {
		return "", fmt.Errorf("%w: lease term %s is under the %s floor",
			output.ErrIncomplete, term, output.MinLeaseTerm)
	}

	return fmt.Sprintf("%d seconds", int(term.Round(time.Second).Seconds())), nil
}

// hangarConflict maps a PostgreSQL error onto the closed set of typed outcomes
// hangar/output already has.
//
// The sentinels are the leaf's, not new ones, so a caller's errors.Is keeps
// working across the boundary between strict input and durable output. The
// schema's own RAISE messages are matched by what they are about rather than by
// their exact text, and anything unrecognised stays infrastructure -- which is
// the honest answer and, importantly, never a cache miss.
func hangarConflict(err error) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}

	switch pgErr.Code {
	case "23505": // unique_violation
		return fmt.Errorf("%w: %s", output.ErrConflict, pgErr.Message)
	case "23503": // foreign_key_violation
		return fmt.Errorf("%w: %s", output.ErrNotFound, pgErr.Message)
	case "23514": // check_violation
		return fmt.Errorf("%w: %s", output.ErrIncomplete, pgErr.Message)
	case "40001", "40P01": // serialization_failure, deadlock_detected
		return fmt.Errorf("%w: %s", ErrHangarLockRetry, pgErr.Message)
	case "P0001": // raise_exception: the schema's own guards
		return hangarGuardRefusal(pgErr.Message)
	}

	return fmt.Errorf("%w: %s", output.ErrInfrastructure, pgErr.Message)
}

func hangarGuardRefusal(message string) error {
	switch {
	case strings.Contains(message, "at risk"),
		strings.Contains(message, "lifetime policy"),
		strings.Contains(message, "lifetime-policy"):
		return fmt.Errorf("%w: %s", output.ErrAtRisk, message)
	case strings.Contains(message, "cannot silently reactivate"),
		strings.Contains(message, "admitted reclaim beside"),
		strings.Contains(message, "exclude one another permanently"),
		strings.Contains(message, "already dispositioned"):
		return fmt.Errorf("%w: %s", output.ErrConflict, message)
	case strings.Contains(message, "stale owner"),
		strings.Contains(message, "backwards"),
		strings.Contains(message, "without advancing"):
		return fmt.Errorf("%w: %s", ErrHangarLockRetry, message)
	}

	return fmt.Errorf("%w: %s", output.ErrIncomplete, message)
}

// The channels the output plane's workers wake on.
//
// NOTIFY accelerates work; it is never the only way work is found. Every worker
// here also has a periodic database-clock fallback no slower than once a
// minute, because component.Runner with a zero interval wakes only on NOTIFY
// and a missed one would strand eligible work until restart.
const (
	HangarOutputCaptureRecoveryChannel = "hangar_output_capture_recovery"
	HangarOutputReleaseChannel         = "hangar_output_release"
	HangarOutputInventoryChannel       = "hangar_output_inventory"
	HangarOutputReclaimChannel         = "hangar_output_reclaim"
)

// HangarOutputNotify wakes a worker after the transaction that created its work
// has committed.
//
// After, not inside. A listener woken by a transaction that then rolled back
// reads state that does not exist and either drops the event or acts on the
// stale one it can still see; the ordering is the whole content of the claim.
//
// And a NOTIFY that fails is a warning, never a rollback. The transaction is
// already committed -- there is nothing left to undo -- and the work is still
// found on the next periodic pass. Returning an error here would tempt a caller
// into unmaking a commit that succeeded, which is a fiction, so this returns
// nothing at all.
func HangarOutputNotify(logger lager.Logger, conn DbConn, channels ...string) {
	for _, channel := range channels {
		if err := conn.Bus().Notify(channel); err != nil {
			logger.Info("failed-to-notify-hangar-output-worker", lager.Data{
				"channel": channel,
				"error":   err.Error(),
				"effect": "none on correctness: the transaction is committed and the work is " +
					"found by the worker's periodic database-clock pass",
			})
		}
	}
}
