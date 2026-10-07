package db

import (
	"context"
	"errors"
	"fmt"
	"time"

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

	return hangarInterval(term), nil
}

// hangarInterval renders a term with NO floor, for the bounded windows that are
// not ownership leases: a stat challenge's freshness window is capped at five
// minutes by the schema and would fail the lease floor, and a challenge is not
// a lease -- nothing is owned for its duration.
func hangarInterval(term time.Duration) string {
	return fmt.Sprintf("%d seconds", int(term.Round(time.Second).Seconds()))
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

// HangarCommitError maps a COMMIT's failure onto the same typed outcomes every
// mid-transaction failure is mapped to.
//
// A DEFERRED constraint trigger raises its refusal at commit and nowhere
// earlier -- hangar_policy_admits_new_protection and hangar_reclaim_exclusion
// are both deferred, because both read rows another transaction may write
// between a Go check and the commit, so a check ahead of them could only ever
// be a guess. The error that comes back is the driver's, carrying the same
// SQLSTATE an immediate RAISE would have carried, and a caller that did not map
// it sees an unclassified failure.
//
// That is not a cosmetic difference. Every caller in this plane distinguishes a
// REFUSAL (an answer: stop, or change something first) from a LOST ANSWER (ask
// again with the same identity). An unmapped JB002 at commit reads as the
// second, and the caller retries the same identity against an integrity finding only an
// operator can reconcile -- a loop with no exit. Mapping it here, once, in the
// adapter every transactor hands out, is what makes "at risk is a refusal" true
// of the whole path rather than of the statements that happen to fail early.
//
// What it deliberately does NOT do is invent a class. An error with no
// SQLSTATE -- a dropped connection, a context cancelled while the server was
// deciding -- comes back unchanged, and its caller still treats it as the
// ambiguous commit it is.
func HangarCommitError(err error) error {
	return hangarConflict(err)
}

// HangarOutputTx is a transaction whose Commit answers in the output leaf's
// typed vocabulary.
//
// It is a wrapper rather than a change to dbTx because the mapping is the
// output plane's reading of a SQLSTATE and dbTx is every other caller's
// transaction too: a JB00x class means nothing outside this plane, and 23505
// already means something different to the callers that check it themselves.
//
// It is exported so that the three adapters which hand a transaction to the
// output coordinator -- the ATC's own wiring, the brine harness's and this
// package's specs -- share one spelling. Three copies of a mapping is three
// chances for one of them to be the one that was not updated.
type HangarOutputTx struct{ Tx }

func (tx HangarOutputTx) Commit() error {
	return HangarCommitError(tx.Tx.Commit())
}

// HangarOutputRepository is the PostgreSQL half of the Hangar output plane.
//
// Every method takes the caller's Tx and nothing that can commit. A consumer's
// binding write and the Hangar operation beside it commit together or roll
// back together, which is only true if the transaction belongs to the caller.
type HangarOutputRepository struct {
	prefix HangarConsumerPrefix
}

// NewHangarOutputRepository takes the consumer-prefix token rather than a
// string, so that a caller that never thought about its own domain locks cannot
// construct one.
func NewHangarOutputRepository(prefix HangarConsumerPrefix) *HangarOutputRepository {
	return &HangarOutputRepository{prefix: prefix}
}

// HangarConsumerPrefixForComponent is the capture component's own token. The
// component advances captures and composes with nobody's binding write, so the
// prefix it holds is empty by construction; it still names itself.
func HangarConsumerPrefixForComponent() HangarConsumerPrefix {
	prefix, err := HangarConsumerPrefixHeld("hangar-output-capture-component")
	if err != nil {
		panic("hangar: the capture component's own consumer prefix is invalid: " + err.Error())
	}

	return prefix
}
