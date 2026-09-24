package composition

import (
	"context"
	"database/sql"
	"errors"

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
	RunID int

	// Number is the run's number within its template -- the ordinal a person
	// sees in the web and passes to fly, as distinct from RunID, which is a
	// primary key nothing outside the database is addressed by. The port
	// returns it on both paths, so this package never reads pipeline_runs.
	Number int

	Replayed bool
}

// Service admits child runs through the port.
type Service struct {
	admitter runs.Admitter
	epoch    int64
}

// NewService builds the service over the port and the activation epoch this
// server speaks for. Zero is a server that cannot admit versioned Runs; every
// admission is then refused with runs.ErrVersionedAdmissionUnavailable.
func NewService(admitter runs.Admitter, epoch int64) *Service {
	return &Service{admitter: admitter, epoch: epoch}
}

// Admit admits a child run for the request's call identity, or re-attaches to
// the one already admitted for it.
//
// There is exactly one dedup path and it is core's (requirement 46): the
// versioned port keys the invocation on ContractKey(build_id, plan_id), scoped
// to the calling build's team and pipeline, and answers whether it created the
// Run or replayed it. composition_calls and composition_iterations are a join
// recording which call the Run belongs to and the sealed-input digest the call
// presented. They are written after the port has decided, in the same
// transaction, and they follow its decision: no consumer row can admit, refuse
// or suppress a Run.
//
// The digest is compared, never keyed on. A replay whose digest moved is a
// typed conflict, returned before commit, so nothing is recorded.
func (s *Service) Admit(ctx context.Context, req Request) (Result, error) {
	tx, err := s.admitter.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback()

	run, replayed, err := s.admitter.AdmitVersionedRun(ctx, tx, runs.Admission{
		Template:  req.Template,
		Params:    req.Params,
		Principal: req.Principal,

		ContractKey: ContractKey(req.BuildID, req.PlanID),
	}, s.epoch)
	if err != nil {
		return Result{}, err
	}

	if replayed {
		err = s.verify(ctx, tx, req, run)
	} else {
		err = s.record(ctx, tx, req, run)
	}
	if err != nil {
		return Result{}, err
	}

	if err := tx.Commit(); err != nil {
		return Result{}, err
	}

	return Result{RunID: run.ID, Number: run.Number, Replayed: replayed}, nil
}

// record writes the join to the Run the port decided on. A stale call row for
// the same (build_id, plan_id) -- one recorded before run_pipeline admitted
// through the versioned port, or one whose iteration was lost -- is repointed
// at the admitted Run rather than allowed to refuse it.
func (s *Service) record(ctx context.Context, tx runs.Tx, req Request, run runs.Run) error {
	var callID int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO composition_calls (build_id, plan_id, input_digest)
		VALUES ($1, $2, $3)
		ON CONFLICT (build_id, plan_id) DO UPDATE SET input_digest = EXCLUDED.input_digest
		RETURNING id
	`, req.BuildID, string(req.PlanID), req.InputDigest).Scan(&callID)
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO composition_iterations (call_id, ordinal, run_id) VALUES ($1, 1, $2)
		ON CONFLICT (call_id, ordinal) DO UPDATE SET run_id = EXCLUDED.run_id
	`, callID, run.ID)

	return err
}

// verify checks a replay against the digest the admitting call recorded. A
// replay whose join is missing, or names another Run, is re-recorded against
// the Run the port replayed: the key decided, and the join only follows it.
func (s *Service) verify(ctx context.Context, tx runs.Tx, req Request, run runs.Run) error {
	var (
		recordedDigest string
		recordedRunID  int
	)

	err := tx.QueryRowContext(ctx, `
		SELECT c.input_digest, i.run_id
		FROM composition_calls c
		JOIN composition_iterations i ON i.call_id = c.id AND i.ordinal = 1
		WHERE c.build_id = $1 AND c.plan_id = $2
	`, req.BuildID, string(req.PlanID)).Scan(&recordedDigest, &recordedRunID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && recordedRunID != run.ID) {
		return s.record(ctx, tx, req, run)
	}
	if err != nil {
		return err
	}

	if recordedDigest != req.InputDigest {
		return DigestConflictError{Recorded: recordedDigest, Presented: req.InputDigest}
	}

	return nil
}
