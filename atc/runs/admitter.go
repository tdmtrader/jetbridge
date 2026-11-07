package runs

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
)

// Admitter is core's published run-admission surface.
//
// A consumer opens the transaction, then admits a versioned Run inside it, so
// it can commit its own rows in the same transaction as the Run. Every Run is
// v2; there is no other class. LookupRun reads a previously admitted Run
// through that same transaction boundary. ObserveRun and CancelRun act on an
// admitted Run outside any caller's transaction.
type Admitter interface {
	SetCredentialHandoffConfig(CredentialHandoffConfig)
	InspectCredentialHandoff(context.Context, TemplateRef, Principal, int, string) (atc.RunCredentialSession, error)
	HandoffCredentials(context.Context, TemplateRef, Principal, int, string, io.ReadCloser) (atc.RunCredentialSession, error)
	SetInputUploadConfig(InputUploadConfig)
	UploadInput(context.Context, TemplateRef, Principal, string, io.Reader) (atc.RunInputSource, error)
	// SetSealedInputAuthority is startup wiring; it supplies no public mint route.
	SetSealedInputAuthority(*runinput.Authority)
	// SetOutputPlane is startup wiring: whether this control plane has a
	// Hangar output plane. No Run is admitted unless it is enabled and in
	// service.
	SetOutputPlane(bool)
	// Begin opens a transaction the consumer owns and must finish.
	Begin(context.Context) (Transaction, error)

	// LookupRun reads an already-admitted run by id, inside the caller's
	// transaction. It is a read and nothing else: it creates nothing, decides
	// no authorization, and refuses an id that names no row.
	LookupRun(ctx context.Context, tx Tx, runID int) (Run, error)

	// AdmitVersionedRun returns whether the
	// invocation already committed. The caller still owns the transaction.
	AdmitVersionedRun(context.Context, Tx, Admission) (Run, bool, error)

	// ObserveRun reads a run's status for a principal entitled to view it:
	// one whose team owns the run's template. It opens and finishes its own
	// short transaction, so it must never be called while the caller holds
	// one from Begin.
	ObserveRun(ctx context.Context, runID int, principal Principal) (RunStatus, error)

	// CancelRun requests cancellation of a run for a principal entitled to
	// cancel it, through the one operation that records the cancel fence. It
	// returns nil when the request is recorded and also when the run is
	// already being cancelled or already terminal: either way the caller's
	// wish holds. Like ObserveRun it uses its own transaction.
	CancelRun(ctx context.Context, runID int, principal Principal, reason string) error
}

type admitter struct {
	conn              db.DbConn
	runFactory        db.PipelineRunFactory
	teamFactory       db.TeamFactory
	displayUserIds    atc.DisplayUserIdGenerator
	customRoles       map[string]string
	sealedInputs      *runinput.Authority
	outputPlane       bool
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

func (a *admitter) SetOutputPlane(enabled bool) { a.outputPlane = enabled }

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
// which is the deadlock AdmitVersionedRun's doc comment describes at length. The
// factory's transaction-scoped readers are unexported, so there is nothing to
// reuse. Five columns through the caller's Tx is the whole of it.
//
// Unlike AdmitVersionedRun this does not bridge back to db.Tx and so does not refuse a
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
		Scan(&run.ID, &run.Number, &run.TemplateID, &run.CreatedBy, &payloadID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrRunNotFound
		}

		return Run{}, err
	}

	run.PayloadID = int(payloadID.Int64)

	return run, nil
}

// observedRunQuery reads a run's identity, its status and its cancel fence,
// with the name of the team that owns its template, which is what
// authorization is decided against. One statement, so the team a run is
// authorized against and the status reported are one snapshot.
const observedRunQuery = `
	SELECT r.id, r.number, r.template_pipeline_id, r.created_by, payload.id,
	       r.status, r.cancel_requested_at IS NOT NULL, owner.name
	FROM pipeline_runs r
	JOIN pipelines tp ON tp.id = r.template_pipeline_id
	JOIN teams owner ON owner.id = tp.team_id
	LEFT JOIN pipelines payload ON payload.pipeline_run_id = r.id
	WHERE r.id = $1
`

// ObserveRun reads a run's status, for a principal who may view the runs of
// the team that owns its template.
//
// It takes no Tx because nothing a caller holds is a place to read status
// from: a waiting step polls, and between polls it holds no connection. The
// transaction opened here is short and finished before return, which keeps
// the port within the one-connection budget so long as the caller does not
// hold Begin's transaction at the same time.
func (a *admitter) ObserveRun(ctx context.Context, runID int, principal Principal) (RunStatus, error) {
	tx, err := a.conn.BeginTx(ctx, nil)
	if err != nil {
		return RunStatus{}, err
	}
	defer db.Rollback(tx)

	status, err := a.authorizedRun(ctx, tx, runID, principal, atc.GetPipelineRun)
	if err != nil {
		return RunStatus{}, err
	}

	return status, tx.Commit()
}

// CancelRun requests cancellation of a run, for a principal who may cancel
// the runs of the team that owns its template.
//
// The fence is written by AcceptRunCancellation and nothing else, which
// architecture_test.go pins; this is one more caller of it, in the shape the
// HTTP cancel route uses. All three of its outcomes -- accepted, already
// requested, already terminal -- are nil here: a caller that asked for a run
// to stop has what it asked for in each, and a step cancelling a child on its
// way out has nothing to do differently for any of them. A caller that wants
// to know which reads the run with ObserveRun.
//
// An empty reason records none.
func (a *admitter) CancelRun(ctx context.Context, runID int, principal Principal, reason string) error {
	var why *string
	if reason != "" {
		why = &reason
	}

	tx, err := a.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)

	if _, err := a.authorizedRun(ctx, tx, runID, principal, atc.CancelPipelineRun); err != nil {
		return err
	}

	requester, err := cancelRequester(principal)
	if err != nil {
		return err
	}

	if _, err := a.runFactory.AcceptRunCancellation(ctx, tx, runID, requester, why); err != nil {
		return err
	}

	return tx.Commit()
}

// authorizedRun reads a run and decides whether the principal may take the
// action on it.
//
// The team is the one that owns the run's template, read in the same
// statement as the run, and the decision is the same authorizeAction
// admission makes, so a build is held against its builds row here too.
//
// A run that does not exist is refused exactly as another team's run is, so
// the port is no oracle over run ids: ErrUnauthorized for everyone but an
// admin, who is entitled to every team's answer and so is told
// ErrRunNotFound -- the same split resolveTemplate makes for a missing team.
func (a *admitter) authorizedRun(ctx context.Context, tx db.Tx, runID int, principal Principal, action string) (RunStatus, error) {
	var (
		status    RunStatus
		payloadID sql.NullInt64
		state     string
		teamName  string
	)

	err := tx.QueryRowContext(ctx, observedRunQuery, runID).Scan(
		&status.Run.ID, &status.Run.Number, &status.Run.TemplateID, &status.Run.CreatedBy, &payloadID,
		&state, &status.CancellationRequested, &teamName)
	if errors.Is(err, sql.ErrNoRows) {
		// No team to authorize against. The empty name authorizes nobody but
		// an admin, through the accessor's own short-circuit.
		auth, err := a.authorizeAction(tx, "", principal, action)
		if err != nil {
			return RunStatus{}, err
		}
		if auth.isAdmin {
			return RunStatus{}, ErrRunNotFound
		}

		return RunStatus{}, ErrUnauthorized
	}
	if err != nil {
		return RunStatus{}, err
	}

	if _, err := a.authorizeAction(tx, teamName, principal, action); err != nil {
		return RunStatus{}, err
	}

	status.Run.PayloadID = int(payloadID.Int64)
	status.Status = atc.RunStatus(state)

	return status, nil
}

// buildRequesterPrefix is under the "system:" prefix the HTTP cancel route
// reserves for requesters the server records itself and refuses to any
// caller, so no person's subject can pass for a build.
const buildRequesterPrefix = "system:build/"

// cancelRequester is the requester recorded on the cancel fence.
//
// A build is its id under the reserved prefix: the id is what the builds row,
// and the parent's log, are addressed by. A person is their verified subject,
// as on the HTTP route, and a subject claiming the reserved prefix is refused
// there and here. A landing queue is its team and name under the same prefix.
func cancelRequester(principal Principal) (string, error) {
	switch {
	case principal.Build != nil:
		return buildRequesterPrefix + strconv.Itoa(principal.Build.BuildID), nil
	case principal.Queue != nil:
		return "system:landing-queue/" + principal.Queue.TeamName + "/" + principal.Queue.QueueName, nil
	}

	subject, _ := principal.Claims["sub"].(string)
	if subject == "" || strings.HasPrefix(subject, "system:") {
		return "", ErrUnauthorized
	}

	return subject, nil
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
// causal chain. The one-hop case is the case a person writes by accident, and it is refused
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
	if auth.caller.templateID != 0 && auth.caller.templateID == template.ID() {
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
	case errors.Is(err, atc.ErrRunOutputPlaneNotInService):
		return atc.ErrRunOutputPlaneNotInService
	case errors.Is(err, atc.ErrRunResultsUnavailable):
		return atc.ErrRunResultsUnavailable
	case errors.Is(err, atc.ErrRunInputUnavailable):
		return atc.ErrRunInputUnavailable
	case errors.Is(err, atc.ErrInvalidRunInputs):
		return atc.ErrInvalidRunInputs
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
	payloadID, _ := creation.Run.PayloadID()

	return Run{
		ID:         creation.Run.ID(),
		Number:     creation.Run.Number(),
		TemplateID: creation.Run.TemplateID(),
		PayloadID:  payloadID,
		CreatedBy:  creation.Run.CreatedBy(),
	}
}
