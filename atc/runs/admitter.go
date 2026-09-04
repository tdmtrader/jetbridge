package runs

import (
	"context"
	"errors"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// Admitter is core's published run-admission surface.
//
// Two operations, and the pairing is the design: a consumer opens the
// transaction, so it can commit its own rows in the same one as the run.
type Admitter interface {
	// Begin opens a transaction the consumer owns and must finish.
	Begin(context.Context) (Transaction, error)

	// AdmitRun admits one run of one template inside the caller's
	// transaction. It does not commit and does not roll back.
	AdmitRun(context.Context, Tx, Admission) (Run, error)
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
// Steps 2 and 3 read on the connection pool rather than on tx, exactly as the
// HTTP create path does -- there the accessor is built and the pipeline
// resolved before the run factory opens its transaction at all. The
// consequence is worth stating: while a caller holds a transaction from Begin,
// this call needs a second connection from the same pool. Production sizes the
// pool from --max-conns; a test that pins it to one connection deadlocks here,
// which is the tripwire working, not a defect to route around.
func (a *admitter) AdmitRun(ctx context.Context, tx Tx, adm Admission) (Run, error) {
	if adm.ContractKey == "" {
		return Run{}, ErrMissingContractKey
	}

	createdBy, err := a.authorize(adm.Template.Team, adm.Principal)
	if err != nil {
		return Run{}, err
	}

	pipeline, err := a.resolveTemplate(adm.Template)
	if err != nil {
		return Run{}, err
	}

	// The one place the port bridges its own interface back to the concrete
	// transaction type the run factory names. Keeping it to one line, and
	// refusing rather than panicking, is what makes a foreign Tx a diagnosable
	// mistake instead of a crash.
	dbTx, ok := tx.(db.Tx)
	if !ok {
		return Run{}, ForeignTransactionError{}
	}

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

// resolveTemplate turns a reference into the pipeline to admit against.
//
// It runs after authorization, so a missing team here is unreachable in
// practice -- an unresolvable team has no roles and cannot authorize. It is
// still reported as ErrUnauthorized rather than ErrTemplateNotFound, so that
// the one path that could reach it (a team deleted between the two reads)
// cannot answer a question the principal was not entitled to ask.
func (a *admitter) resolveTemplate(ref TemplateRef) (db.Pipeline, error) {
	team, found, err := a.teamFactory.FindTeam(ref.Team)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrUnauthorized
	}

	pipeline, found, err := team.Pipeline(ref.Pipeline)
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
