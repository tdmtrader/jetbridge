package runs

import (
	"context"
	"database/sql"
	"errors"
	"io"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
)

// Admitter is core's published run-admission surface.
//
// A consumer opens the transaction, then chooses legacy or versioned admission,
// so it can commit its own rows in the same transaction as the Run. LookupRun
// reads a previously admitted Run through that same transaction boundary.
type Admitter interface {
	SetCredentialHandoffConfig(CredentialHandoffConfig)
	InspectCredentialHandoff(context.Context, TemplateRef, Principal, int, string, int64) (atc.RunCredentialSession, error)
	HandoffCredentials(context.Context, TemplateRef, Principal, int, string, int64, io.ReadCloser) (atc.RunCredentialSession, error)
	SetInputUploadConfig(InputUploadConfig)
	UploadInput(context.Context, TemplateRef, Principal, string, int64, io.Reader) (atc.RunInputSource, error)
	// SetSealedInputAuthority is startup wiring; it supplies no public mint route.
	SetSealedInputAuthority(*runinput.Authority)
	// Begin opens a transaction the consumer owns and must finish.
	Begin(context.Context) (Transaction, error)

	// AdmitRun admits one run of one template inside the caller's
	// transaction. It does not commit and does not roll back.
	AdmitRun(context.Context, Tx, Admission) (Run, error)

	// LookupRun reads an already-admitted run by id, inside the caller's
	// transaction. It is a read and nothing else: it creates nothing, decides
	// no authorization, and refuses an id that names no row.
	LookupRun(ctx context.Context, tx Tx, runID int) (Run, error)

	// AdmitVersionedRun requires the activation epoch and returns whether the
	// invocation already committed. The caller still owns the transaction.
	AdmitVersionedRun(context.Context, Tx, Admission, int64) (Run, bool, error)
}

type admitter struct {
	conn              db.DbConn
	runFactory        db.PipelineRunFactory
	teamFactory       db.TeamFactory
	displayUserIds    atc.DisplayUserIdGenerator
	customRoles       map[string]string
	sealedInputs      *runinput.Authority
	inputUploads      InputUploadConfig
	credentialHandoff CredentialHandoffConfig
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

func (a *admitter) SetSealedInputAuthority(authority *runinput.Authority) { a.sealedInputs = authority }

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
	// The operator's hold comes first, ahead of even the contract key, for the
	// reason the HTTP create route gives for putting it ahead of reading the
	// request body: the hold is a property of the server rather than of the
	// call, so nothing about the call is weighed under a server that is not
	// creating runs at all. atc.EnablePipelineRunCreation is a process-wide
	// value assigned once in atccmd before anything is served, so reading it
	// here is reading the same setting the route reads, not a second copy of
	// it.
	//
	// It lives at the port rather than at each consumer because the route was
	// the only thing consulting it, and a second creation path that forgot to
	// is exactly the drift this package exists to make impossible. Refusing
	// before the template is resolved and before CreateRunInTx is reached
	// keeps "no row, no number, no payload, no notification" true of the
	// in-process path as well: the caller's transaction is already open, and
	// whatever it wrote before asking goes back with its rollback.
	if !atc.EnablePipelineRunCreation {
		return Run{}, atc.ErrPipelineRunCreationDisabled
	}

	if adm.ContractKey == "" {
		return Run{}, ErrMissingContractKey
	}
	// Inputs, causation and correlation are v2 caller intent. A legacy Run has
	// nowhere to retain them, so they are refused rather than dropped.
	if len(adm.Inputs) != 0 || adm.CausedByRun != nil || adm.Correlation != "" {
		return Run{}, ErrUnsupportedInvocation
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

	// After the template is resolved, because the check is an identity
	// comparison between it and the caller's own pipeline, and before
	// anything is created, because a refused admission writes nothing.
	if err := refuseDirectRecursion(auth, pipeline); err != nil {
		return Run{}, err
	}

	run, _, err := a.createAdmission(ctx, dbTx, pipeline, adm, auth.createdBy, db.RunCreationOpts{})
	return run, err
}

func (a *admitter) createAdmission(ctx context.Context, dbTx db.Tx, pipeline db.Pipeline, adm Admission, createdBy string, opts db.RunCreationOpts) (Run, bool, error) {
	if adm.BeforeCommit != nil {
		// The callback is handed the port's Tx and the port's Run. It runs
		// inside this same transaction, after the run and its payload exist
		// and before the caller commits; its error aborts creation, which is
		// the guarantee the underlying seam already makes and this preserves
		// rather than flattens.
		opts.BeforeCommit = func(hookTx db.Tx, creation db.RunCreation) error {
			return adm.BeforeCommit(hookTx, portRun(creation))
		}
	}

	creation, err := a.runFactory.CreateRunInTx(ctx, dbTx, pipeline,
		db.RunParams{Vars: adm.Params}, createdBy, opts)
	if err != nil {
		return Run{}, false, refusal(err)
	}

	return portRun(creation), creation.Replayed, nil
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

// refuseDirectRecursion refuses the two loops a single admission can see.
//
// Both are identity comparisons against the resolved template's pipeline id,
// made from facts the authorization step already read off the caller's build
// row, so neither costs a read.
//
// The first is a build of the template itself asking for a run of it, which
// atc/db makes unreachable today and which is enforced here anyway -- see
// ErrCallerIsTemplate. The second is the one that actually happens: a
// template whose entry job carries a `run_pipeline` naming itself, which reads
// as harmless in the config and produces an unbounded chain of runs the first
// time it is admitted -- the caller is then a build of a payload pipeline,
// and that payload's run names the template it materialized from.
//
// Ids rather than team-and-name, although the rule is usually stated that way.
// A pipeline id is the team and the name and the instance vars together, which
// is what has to agree for two references to mean the same pipeline; comparing
// names would need the fold and the instance vars restated here, and would get
// one of them wrong eventually.
//
// This bounds direct recursion only -- one hop, from the facts one admission
// can see. A cycle through two templates that call each other leaves no trace
// on either build row, and nothing here can detect it. Detecting it needs the
// causal chain. Versioned admission now retains that edge
// (Admission.CausedByRun), but this legacy path does not carry it, so
// multi-hop detection waits until run_pipeline admits through the versioned
// port. The one-hop case is not a down payment on
// that work; it is the case a person writes by accident, and it is refused
// today rather than left until the track that will generalize it.
//
// Only a build principal reaches either check. A person asking over HTTP has
// no calling build, so there is no loop to be in -- creating a run of a
// template from a job of that same template is a thing a person may do once,
// deliberately, and nothing about it recurs.
func refuseDirectRecursion(auth authorization, template db.Pipeline) error {
	if auth.caller == nil {
		return nil
	}

	// Zero means a one-off build, which belongs to no pipeline. It can be in
	// neither loop, and comparing zero against a real id would be comparing
	// "no pipeline" with a pipeline.
	if auth.caller.pipelineID != 0 && auth.caller.pipelineID == template.ID() {
		return ErrCallerIsTemplate
	}

	// Zero means the caller's pipeline is not a run's payload at all.
	if auth.caller.templatePipelineID != 0 && auth.caller.templatePipelineID == template.ID() {
		return ErrCallerIsRunOfTemplate
	}

	return nil
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
	case errors.Is(err, db.ErrRunInvocationConflict):
		return ErrInvocationConflict
	case errors.Is(err, db.ErrRunCauseUnavailable):
		return ErrRunCauseUnavailable
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

func portRun(creation db.RunCreation) Run {
	payloadID, _ := creation.Run.InstancePipelineID()

	return Run{
		ID:                 creation.Run.ID(),
		Number:             creation.Run.Number(),
		TemplatePipelineID: creation.Run.TemplatePipelineID(),
		PayloadPipelineID:  payloadID,
		CreatedBy:          creation.Run.CreatedBy(),
	}
}
