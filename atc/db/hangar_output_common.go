package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The work every Hangar output repository method does the same way, in one
// place: reading a row over the two methods output.Tx has, turning a lease term
// into something SQL will accept, mapping a PostgreSQL failure onto the leaf's
// typed outcomes, and waking a worker after a commit.
//
// Each was written three or four times before it lived here, and each is
// somewhere a second spelling would be a second behaviour: a lease term rounded
// differently, a conflict that mapped to the wrong sentinel, or a notification
// sent before the commit it announces.
//
// Every deadline in this plane is still measured on the database clock -- Reqs
// 10, 11, 36, 39 and 48 all say so, and a node whose clock drifts must not be
// able to expire its own hold -- but it is read where it is used, as `now()`
// inside the statement that depends on it, which is the reading that cannot
// have drifted by the time the row is written.

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

// The four classes of refusal the output plane's schema raises, as SQLSTATEs.
//
// A class on the RAISE, not a substring of its message, because the messages
// are shared: "backwards" is in six trigger functions, "exclude one another
// permanently" in two, and "at risk" -- which the old map looked for -- was in
// none of them, because the state is spelled `at_risk`. A substring can only
// ever be a guess about which refusal happened, and the guess this system was
// making told a stale owner and a re-enabled disabled epoch to retry.
//
// The letters are not the obvious `HG`: PostgreSQL reserves SQLSTATE classes
// beginning 0-4 and A-H for standard codes, so a code in that range is one
// release away from meaning something the server chose.
const (
	hangarRefusalConflict   = "JB001"
	hangarRefusalAtRisk     = "JB002"
	hangarRefusalStaleFence = "JB003"
	hangarRefusalIncomplete = "JB004"
)

// hangarConflict maps a PostgreSQL error onto the closed set of typed outcomes
// hangar/output already has.
//
// The sentinels are the leaf's, not new ones, so a caller's errors.Is keeps
// working across the boundary between strict input and durable output. The
// schema's own refusals arrive classified by the RAISE that made them, and
// anything unrecognised stays infrastructure -- which is the honest answer and,
// importantly, never a cache miss.
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
		// The only two that mean "run this again". PostgreSQL aborted a
		// transaction it could have run; the same statements against the same
		// rows may well commit next time.
		return fmt.Errorf("%w: %s", ErrHangarLockRetry, pgErr.Message)
	case hangarRefusalConflict:
		return fmt.Errorf("%w: %s", output.ErrConflict, pgErr.Message)
	case hangarRefusalAtRisk:
		return fmt.Errorf("%w: %s", output.ErrAtRisk, pgErr.Message)
	case hangarRefusalStaleFence:
		// Req 10: a stale owner may not seal, publish, sign/register, finalize
		// or release, and plan.md says a lost CAS is a typed stale refusal and
		// never a retry loop. Re-running produces the same refusal; the owner
		// has to take the lease over first, which advances the fence, which is
		// a different transaction with different facts.
		return fmt.Errorf("%w: %s", executioncontrol.ErrStaleFence, pgErr.Message)
	case hangarRefusalIncomplete:
		return fmt.Errorf("%w: %s", output.ErrIncomplete, pgErr.Message)
	case "P0001":
		// raise_exception with no class: a refusal somebody added without
		// saying which kind. It is still a refusal, so it is not a retry and
		// not a conflict it never claimed to be.
		return fmt.Errorf("%w: %s", output.ErrIncomplete, pgErr.Message)
	}

	return fmt.Errorf("%w: %s", output.ErrInfrastructure, pgErr.Message)
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
