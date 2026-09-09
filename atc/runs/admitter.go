package runs

import (
	"context"
	"database/sql"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// Admitter is core's published run-admission surface.
//
// The first two operations are a pair, and the pairing is the design: a
// consumer opens the transaction, so it can commit its own rows in the same
// one as the run. The third is the read the pair implies -- a consumer that
// recorded a run id and comes back later holds an id and nothing else, and
// core will not have it read pipeline_runs for the rest.
type Admitter interface {
	// Begin opens a transaction the consumer owns and must finish.
	Begin(context.Context) (Transaction, error)

	// AdmitRun admits one run of one template inside the caller's
	// transaction. It does not commit and does not roll back.
	AdmitRun(context.Context, Tx, Admission) (Run, error)

	// LookupRun reads an already-admitted run by id, inside the caller's
	// transaction. It is a read and nothing else: it creates nothing, decides
	// no authorization, and refuses an id that names no row.
	LookupRun(ctx context.Context, tx Tx, runID int) (Run, error)
}

type admitter struct {
	conn           db.DbConn
	runFactory     db.PipelineRunFactory
	teamFactory    db.TeamFactory
	displayUserIds atc.DisplayUserIdGenerator
	customRoles    map[string]string
}

// NewAdmitter builds the port over its core collaborators.
//
// This constructor names atc/db types, and nothing can be done about that: a
// port over CreateRunInTx has to name the factory that owns it. That is why
// the package's rule is stated over exported *method* signatures. It is the
// gc.NewDestroyer shape, and like that one it is called at the composition
// root and in suite files -- never by a consumer, which holds an Admitter and
// never builds one.
//
// customRoles is the operator's role mapping, the same map the API's
// authorization uses.
func NewAdmitter(
	conn db.DbConn,
	runFactory db.PipelineRunFactory,
	teamFactory db.TeamFactory,
	displayUserIds atc.DisplayUserIdGenerator,
	customRoles map[string]string,
) Admitter {
	return &admitter{
		conn:           conn,
		runFactory:     runFactory,
		teamFactory:    teamFactory,
		displayUserIds: displayUserIds,
		customRoles:    customRoles,
	}
}

// Begin opens the transaction admission runs in.
//
// The assignment of db.Tx to Transaction is the whole check that the port's
// interfaces are structural: db.Tx declares twelve methods, five of which are
// these, so this compiles only because no adaptation is needed at the call
// site. Widen Tx or Transaction beyond what db.Tx offers and this line stops
// building, which is the correct moment to find out.
func (a *admitter) Begin(ctx context.Context) (Transaction, error) {
	return a.conn.BeginTx(ctx, nil)
}

// AdmitRun admits one run of the referenced template inside tx.
//
// The order of the first three steps is the contract, not an implementation
// detail:
//
//  1. the contract key is checked before any row is touched, so an admission
//     that could never be attributed to a call record does not create one;
//  2. authorization is decided against the reference's team, before the
//     template is resolved, so that an unauthorized principal learns nothing
//     about whether the template exists;
//  3. only then is the template resolved, which is what lets not-found be its
//     own refusal rather than arriving as "not a template".
//
// Every read those steps make goes through tx, and that is a correctness
// requirement rather than tidiness. The caller has held a pooled connection
// since Begin and will hold it until it commits, so a read on the pool from
// here would want a *second* connection while the first is still checked out.
// N concurrent admissions against a pool of N would then each hold one and wait
// for another that nobody is going to release, and because the factories' pool
// reads take no context, nothing would time out: the process would stop rather
// than fail. This is not the shape of the HTTP create path and cannot be --
// there the accessor is built and the pipeline resolved before any transaction
// exists at all. atc/runs/connection_budget_test.go pins the budget at one
// connection.
func (a *admitter) AdmitRun(ctx context.Context, tx Tx, adm Admission) (Run, error) {
	if adm.ContractKey == "" {
		return Run{}, ErrMissingContractKey
	}

	// The one place the port bridges its own interface back to the concrete
	// transaction type the run factory and the tx-scoped reads name. Keeping it
	// to one line, and refusing rather than panicking, is what makes a foreign
	// Tx a diagnosable mistake instead of a crash. It comes before the reads
	// because they need it too, and a foreign transaction should be refused
	// before anything is read on the caller's behalf.
	dbTx, ok := tx.(db.Tx)
	if !ok {
		return Run{}, ForeignTransactionError{}
	}

	auth, err := a.authorize(dbTx, adm.Template.Team, adm.Principal)
	if err != nil {
		return Run{}, err
	}

	pipeline, err := a.resolveTemplate(dbTx, auth, adm.Template)
	if err != nil {
		return Run{}, err
	}

	createdBy := auth.createdBy

	opts := db.RunCreationOpts{}
	if adm.BeforeCommit != nil {
		// The callback is handed the port's Tx and the port's Run. It runs
		// inside this same transaction, after the run and its payload exist
		// and before the caller commits; its error aborts creation, which is
		// the guarantee the underlying seam already makes and this preserves
		// rather than flattens.
		opts.BeforeCommit = func(hookTx db.Tx, creation db.RunCreation) error {
			return adm.BeforeCommit(hookTx, portRun(creation, createdBy))
		}
	}

	creation, err := a.runFactory.CreateRunInTx(ctx, dbTx, pipeline,
		db.RunParams{Vars: adm.Params}, createdBy, opts)
	if err != nil {
		return Run{}, refusal(err)
	}

	return portRun(creation, createdBy), nil
}

// lookupRunQuery reads one run's identity, and the identity is all of it.
//
// The columns are exactly the fields of Run and no others: a consumer that
// reads a run back learns what a consumer that admitted one learns, and the
// status, the params and the timestamps stay on core's side of the boundary
// where the model that interprets them lives. The left join is the same one
// atc/db's own pipelineRunsQuery uses to reach the payload pipeline, which is
// how a run with no payload row -- not a state admission produces, but not one
// a read may crash on either -- comes back as a zero id rather than an error.
const lookupRunQuery = `
	SELECT r.id, r.number, r.template_pipeline_id, r.created_by, payload.id
	FROM pipeline_runs r
	LEFT JOIN pipelines payload ON payload.pipeline_run_id = r.id
	WHERE r.id = $1
`

// LookupRun reads an already-admitted run by id.
//
// The read is a statement of this package's own rather than a call into
// PipelineRunFactory, and the reason is the connection budget. The factory's
// two by-id readers, GetRun and GetRunByID, run on the pool: called from here
// they would want a second connection while the caller still holds the first,
// which is the deadlock AdmitRun's doc comment describes at length. The
// factory's transaction-scoped readers are unexported, so there is nothing to
// reuse. Five columns through the caller's Tx is the whole of it.
//
// Unlike AdmitRun this does not bridge back to db.Tx and so does not refuse a
// foreign transaction: it hands the handle to nothing, it just reads through
// it. A Tx from somewhere else is a transaction the caller owns and a
// perfectly good place to read from, and refusing it would be a rule with no
// failure behind it.
//
// It decides no authorization, and that is not an omission. A consumer can
// only reach this with an id the port itself handed back, on a run the port
// itself authorized when it admitted it; there is no name to guess and no
// existence oracle to protect, which is why an unknown id is ErrRunNotFound
// and not ErrUnauthorized.
func (a *admitter) LookupRun(ctx context.Context, tx Tx, runID int) (Run, error) {
	var (
		run       Run
		payloadID sql.NullInt64
	)

	err := tx.QueryRowContext(ctx, lookupRunQuery, runID).
		Scan(&run.ID, &run.Number, &run.TemplatePipelineID, &run.CreatedBy, &payloadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrRunNotFound
		}

		return Run{}, err
	}

	run.PayloadPipelineID = int(payloadID.Int64)

	return run, nil
}

// resolveTemplate turns a reference into the pipeline to admit against.
//
// The team it resolves against is the one authorization already picked out of
// its single read, so there is no second read and no window for the two to
// disagree.
//
// A nil team is reachable for exactly one principal: an admin, because
// accessor.IsAuthorized short-circuits on isAdmin and so passes for a team name
// that is not there at all. Telling that principal the team does not exist
// gives nothing away -- an admin is entitled to the answer for every team --
// so it gets ErrTemplateNotFound rather than a refusal that would send them
// looking for a permissions problem they do not have. For anyone else a nil
// team is unreachable, since a name with no team has no roles; ErrUnauthorized
// is what it would deserve if that ever stopped being true, so that is what it
// keeps.
func (a *admitter) resolveTemplate(tx db.Tx, auth authorization, ref TemplateRef) (db.Pipeline, error) {
	if auth.team == nil {
		if auth.isAdmin {
			return nil, ErrTemplateNotFound
		}

		return nil, ErrUnauthorized
	}

	pipeline, found, err := auth.team.PipelineInTx(tx, ref.Pipeline)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTemplateNotFound
	}

	return pipeline, nil
}

// refusal re-expresses the run factory's refusals as the port's own.
//
// ErrPipelineRunNotTemplate arriving here after a successful resolve means one
// of two things, and both are "not a template" rather than "not found": either
// the pipeline genuinely is not one, or it was deleted or de-templated between
// the resolve above and the factory's own FOR UPDATE OF p scan. Reporting that
// race as not-found would be a lie about a row that existed a moment ago.
func refusal(err error) error {
	var (
		templateInvalid db.ErrPipelineTemplateInvalid
		invalidParams   atc.InvalidRunParamsError
	)

	switch {
	case errors.Is(err, db.ErrPipelineRunNotTemplate):
		return ErrNotATemplate
	case errors.Is(err, db.ErrPipelineRunInstanced):
		return ErrTemplateInstanced
	case errors.Is(err, db.ErrPipelineRunArchived):
		return ErrTemplateArchived
	case errors.Is(err, db.ErrPipelineRunPaused):
		return ErrTemplatePaused
	case errors.As(err, &templateInvalid):
		return TemplateConfigInvalidError{Err: templateInvalid.Err}
	case errors.As(err, &invalidParams):
		return InvalidParamsError{Err: invalidParams.Err}
	default:
		// Anything else -- a driver error, a consumer's own before-commit
		// refusal -- travels unchanged. Wrapping it would hide the callback's
		// error from the consumer that raised it.
		return err
	}
}

func portRun(creation db.RunCreation, createdBy string) Run {
	payloadID, _ := creation.Run.InstancePipelineID()

	return Run{
		ID:                 creation.Run.ID(),
		Number:             creation.Run.Number(),
		TemplatePipelineID: creation.Run.TemplatePipelineID(),
		PayloadPipelineID:  payloadID,
		CreatedBy:          createdBy,
	}
}
