package composition

import (
	"context"
	"database/sql"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runs"
)

// Request is one node of one build asking for one child run.
type Request struct {
	// BuildID and PlanID are the call identity, and the only things it is
	// derived from.
	BuildID int
	PlanID  atc.PlanID

	Template  runs.TemplateRef
	Params    atc.RunParams
	Principal runs.Principal

	// InputDigest is the sealed inputs of the call, recorded on first
	// admission and verified on every later one. Never part of the key.
	InputDigest string
}

// Result is what the caller gets back: the child run, and whether this call
// admitted it or re-attached to it.
type Result struct {
	RunID    int
	Replayed bool
}

// Service admits child runs through the port.
type Service struct {
	admitter runs.Admitter
}

func NewService(admitter runs.Admitter) *Service {
	return &Service{admitter: admitter}
}

// Admit admits a child run for the request's call identity, or re-attaches to
// the one already admitted for it.
//
// One transaction, and the claim comes first.
//
// Claim-first rather than admit-first is not a preference. If the run were
// admitted first and the call row written afterwards, the loser of a race
// would hit the unique violation with a run already created in its own
// transaction: it would have to abort and start a second one to re-read the
// winner's row, and the call, the iteration and the run would no longer commit
// together. Claiming first means the loser's INSERT blocks on the index before
// anything has been created, and it re-reads the winner inside the same
// transaction it opened.
//
// The unique index is what makes this correct, not the build tracking lock.
// The lock is released while a draining web's waiting goroutine is still
// alive, so two trackers can be live for one build; a dedup resting on it
// passes its test and fails in production. This never takes it.
func (s *Service) Admit(ctx context.Context, req Request) (Result, error) {
	tx, err := s.admitter.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()

	callID, claimed, err := s.claim(ctx, tx, req)
	if err != nil {
		return Result{}, err
	}

	if !claimed {
		// Somebody else owns this call. Under READ COMMITTED the INSERT above
		// did not return until their transaction ended, and a fresh statement
		// takes a fresh snapshot, so their rows are visible now.
		result, err := s.replay(ctx, tx, req)
		if err != nil {
			return Result{}, err
		}
		if err := tx.Commit(); err != nil {
			return Result{}, err
		}

		return result, nil
	}

	run, err := s.admitter.AdmitRun(ctx, tx, runs.Admission{
		Template:  req.Template,
		Params:    req.Params,
		Principal: req.Principal,

		ContractKey: ContractKey(req.BuildID, req.PlanID),

		// Nil at this, the only, call site. The causal edge and its refusals
		// belong to the run contract; this track defines none of them.
		CausedByRun: nil,

		// The admitted run id lands on the iteration row inside the caller's
		// transaction, which is what makes the call, the iteration and the run
		// atomic. Always the first ordinal: this drives no loop and admits no
		// second iteration.
		BeforeCommit: func(hookTx runs.Tx, created runs.Run) error {
			_, err := hookTx.ExecContext(ctx,
				`INSERT INTO composition_iterations (call_id, ordinal, run_id) VALUES ($1, 1, $2)`,
				callID, created.ID)

			return err
		},
	})
	if err != nil {
		return Result{}, err
	}

	if err := tx.Commit(); err != nil {
		return Result{}, err
	}

	return Result{RunID: run.ID, Replayed: false}, nil
}

// claim takes ownership of the call, or reports that someone else has it.
//
// ON CONFLICT DO NOTHING with the conflict target named: Postgres resolves
// that target to the unique index at planning time, so the statement is bound
// to the constraint by name. If a conflicting row is uncommitted the statement
// blocks until that transaction ends -- which is exactly the behaviour the
// replay below depends on.
func (s *Service) claim(ctx context.Context, tx runs.Tx, req Request) (int64, bool, error) {
	rows, err := tx.QueryContext(ctx, `
		INSERT INTO composition_calls (build_id, plan_id, input_digest)
		VALUES ($1, $2, $3)
		ON CONFLICT (build_id, plan_id) DO NOTHING
		RETURNING id
	`, req.BuildID, string(req.PlanID), req.InputDigest)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, false, rows.Err()
	}

	var callID int64
	if err := rows.Scan(&callID); err != nil {
		return 0, false, err
	}

	return callID, true, rows.Err()
}

// replay re-attaches to the run the owner of this call already admitted.
//
// The digest is compared, never keyed on: a moved digest is a typed conflict
// and admits nothing. Note the order -- the conflict is returned before
// anything is admitted, so an implementation that admitted on a moved digest
// would show up as a second run row.
func (s *Service) replay(ctx context.Context, tx runs.Tx, req Request) (Result, error) {
	var (
		recordedDigest string
		runID          int
	)

	err := tx.QueryRowContext(ctx, `
		SELECT c.input_digest, i.run_id
		FROM composition_calls c
		JOIN composition_iterations i ON i.call_id = c.id AND i.ordinal = 1
		WHERE c.build_id = $1 AND c.plan_id = $2
	`, req.BuildID, string(req.PlanID)).Scan(&recordedDigest, &runID)
	if err != nil {
		if err == sql.ErrNoRows {
			return Result{}, ErrCallRecordIncomplete
		}

		return Result{}, err
	}

	if recordedDigest != req.InputDigest {
		return Result{}, DigestConflictError{Recorded: recordedDigest, Presented: req.InputDigest}
	}

	return Result{RunID: runID, Replayed: true}, nil
}
