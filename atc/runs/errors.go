package runs

import "errors"

// The port's refusals, re-expressed.
//
// Every one of these has a counterpart the run factory raises as an atc/db
// value. Re-declaring them here is the point: a consumer distinguishes them
// without importing atc/db, and the port -- not the consumer -- owns the
// mapping. Sentinels where the refusal is a flag; wrapping structs where the
// reason has to reach the caller.
//
// Not all of them are translations. The port makes checks of its own that
// nothing downstream makes -- the contract key is present, the principal is
// one identity rather than two or none, a run id names a row -- and those
// refuse in terms only this package can state.
//
// ErrTemplateNotFound is the one translated refusal whose counterpart does not
// exist, and its absence downstream is the reason it does. The run factory
// maps sql.ErrNoRows from
// its FOR UPDATE OF p scan onto "not a template", so "no such pipeline" and
// "that pipeline is not a template" arrive as one value. This port resolves
// the template from a reference before admitting, so it sees not-found first
// and does not inherit the conflation.
var (
	// ErrTemplateNotFound means no pipeline of that name exists on that team.
	// It is never reported to a principal who is not authorized for the team:
	// see ErrUnauthorized.
	ErrTemplateNotFound = errors.New("template pipeline not found")

	// ErrNotATemplate means the pipeline exists but is not a template.
	//
	// It also covers the narrow race in which a pipeline resolved a moment ago
	// was deleted or stopped being a template before admission locked it.
	ErrNotATemplate = errors.New("pipeline is not a template")

	// ErrTemplateInstanced means the named pipeline is an instance, which
	// cannot be a template.
	ErrTemplateInstanced = errors.New("template pipeline cannot have instance vars")

	// ErrTemplateArchived means the template is archived. It is never
	// collapsed into paused or into not-found.
	ErrTemplateArchived = errors.New("template pipeline is archived")

	// ErrTemplatePaused means the template is paused.
	//
	// Called out because a consumer decision turns on it: a waiting parent
	// whose re-admission hits a paused template is meant to keep waiting
	// rather than to fail, and that is only expressible if paused is
	// distinguishable from archived and from gone.
	ErrTemplatePaused = errors.New("template pipeline is paused")

	// ErrUnauthorized means the principal is not authorized to create runs on
	// the team named in the reference.
	//
	// It is also the answer when the team does not exist, and when the
	// template does not exist but the principal could not have been told
	// either way: admission must not be an existence oracle for another team's
	// pipeline names. It is never collapsed into paused or archived, because
	// those are answers a caller is entitled to act on.
	ErrUnauthorized = errors.New("not authorized to create runs for this team")

	// ErrMissingContractKey means the admission carried no contract key.
	//
	// Presence is the whole of the port's check. It does not verify the key
	// was recorded anywhere -- the record lives in a consumer table, and a
	// SELECT from core into one would breach the boundary this package draws.
	ErrMissingContractKey = errors.New("admission requires a non-empty contract key")

	// ErrPrincipalAmbiguous means the principal set both of its forms, or
	// neither.
	//
	// The two forms are authorized by different rules against different
	// evidence: claims go to the accessor and are weighed against the team's
	// auth config, a build is weighed against its own team name and nothing
	// else. There is no defensible reading of a principal that presents both.
	// Preferring one would silently discard an identity the caller thought it
	// was presenting -- and if the discarded one were the narrower, that is a
	// privilege escalation dressed as a default. An empty principal is refused
	// for the same reason from the other side: it authorizes nothing, and the
	// port will not guess which nothing was meant.
	ErrPrincipalAmbiguous = errors.New("principal must present exactly one of claims and a build")

	// ErrRunNotFound means no run exists with the id LookupRun was given.
	//
	// It is not an authorization answer and does not pretend to be one: a
	// consumer looks a run up by an id it already holds, which it can only
	// hold because the port handed it back at admission, so there is no name
	// to guess at and no oracle to protect. Deleted between the admission and
	// the lookup is the case this actually reports.
	ErrRunNotFound = errors.New("pipeline run not found")
)

// TemplateConfigInvalidError reports a stored template config that no longer
// satisfies template validation at admission time -- a row written before
// save-time validation existed, or edited around it. Not the caller's mistake,
// but the reason has to reach them all the same.
type TemplateConfigInvalidError struct{ Err error }

func (e TemplateConfigInvalidError) Error() string {
	return "invalid pipeline template: " + e.Err.Error()
}

func (e TemplateConfigInvalidError) Unwrap() error { return e.Err }

// InvalidParamsError reports run parameters that do not satisfy the template's
// declared schema. The reason names the parameter, so it wraps rather than
// flattening to a sentinel.
type InvalidParamsError struct{ Err error }

func (e InvalidParamsError) Error() string { return e.Err.Error() }

func (e InvalidParamsError) Unwrap() error { return e.Err }

// CustomRolesInvalidError reports an operator role mapping the port refuses to
// honour -- one that would make creating a run a weaker capability than
// setting a pipeline config.
//
// It is raised at admission rather than at construction so that the refusal is
// observable as an admission outcome. In production atccmd validates the same
// map at startup, so this is a guarantee the port owns rather than a second
// copy of a check it does not.
type CustomRolesInvalidError struct{ Err error }

func (e CustomRolesInvalidError) Error() string {
	return "invalid custom role assignment: " + e.Err.Error()
}

func (e CustomRolesInvalidError) Unwrap() error { return e.Err }

// ForeignTransactionError reports a Tx the port did not open.
//
// AdmitRun has to hand its Tx back to the run factory, which names the
// concrete transaction type, so there is exactly one place where the port
// bridges its own interface back. A value that arrived from somewhere else
// fails there. Refusing is better than panicking: a boundary that crashes the
// process on a misuse is a worse boundary than one that says what happened.
type ForeignTransactionError struct{}

func (ForeignTransactionError) Error() string {
	return "transaction was not opened by this port; use Admitter.Begin"
}

// Refusal marks an error as a refusal rather than a fault.
//
// A refusal is the port answering the caller: a fact about what was asked for
// or about the template's state -- the wrong team, no such template, a paused
// one, params that do not satisfy the declared schema. Retrying changes none
// of it; whoever wrote the call has to change the call. A fault is everything
// else -- a dropped connection, a transaction from the wrong place, a state
// that should be unreachable -- and retrying one of those is exactly the right
// thing to do.
//
// The interface exists so a consumer beyond this package can add its own
// refusals to that set without core naming the consumer. atc/agent/composition
// raises one of its own (a re-attach whose sealed inputs moved) and marks it,
// so IsRefusal answers for it with the dependency pointing the direction
// architecture_test.go requires.
type Refusal interface {
	// AdmissionRefusal has no behaviour. It is here to be written
	// deliberately: an error is a refusal because someone declared it one, not
	// because its message happened to read like one.
	AdmissionRefusal()
}

// refusalSentinels are the flag-shaped refusals, listed once so that IsRefusal
// and its spec cannot drift apart. Everything absent from it -- and from the
// two wrapping types IsRefusal names below -- is a fault.
var refusalSentinels = []error{
	ErrTemplateNotFound,
	ErrNotATemplate,
	ErrTemplateInstanced,
	ErrTemplateArchived,
	ErrTemplatePaused,
	ErrUnauthorized,
}

// IsRefusal reports whether err is a refusal.
//
// The distinction has to be made somewhere, and the caller who needs it cannot
// make it. A build step failing an admission has to tell "your pipeline config
// is wrong" from "the platform is broken": the first belongs on the build's
// stderr and fails the step, the second belongs in the step's returned error,
// where the engine's abort and retry handling can see it and where a raw
// driver message does not land in a build log that is world-readable on a
// public pipeline. Deciding that by hand at each consumer means re-deriving
// this package's vocabulary, incompletely, in a package that cannot even name
// every refusal -- composition's is not core's to name.
//
// So the set is closed here. It is closed conservatively: an error this
// function does not recognize is a fault. An unrecognized fault reported as a
// refusal is a build that failed for a reason nobody can act on and a retry
// that never happened; an unrecognized refusal reported as a fault is a noisy
// error, which is the cheaper mistake.
//
// Wrapping is respected throughout, because a consumer that adds context with
// %w has not stopped being refused.
func IsRefusal(err error) bool {
	if err == nil {
		return false
	}

	for _, sentinel := range refusalSentinels {
		if errors.Is(err, sentinel) {
			return true
		}
	}

	// The two refusals that carry a reason rather than being one.
	var invalidParams InvalidParamsError
	if errors.As(err, &invalidParams) {
		return true
	}

	var invalidTemplate TemplateConfigInvalidError
	if errors.As(err, &invalidTemplate) {
		return true
	}

	var refusal Refusal

	return errors.As(err, &refusal)
}
