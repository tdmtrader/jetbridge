package runs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runinput"
)

var (
	ErrInvalidInvocationKey  = errors.New("invalid invocation key")
	ErrInvocationConflict    = errors.New("invocation conflict")
	ErrUnsupportedInvocation = errors.New("unsupported invocation fields")
	ErrInvalidCorrelation    = errors.New("invalid invocation correlation")
	// ErrRunCauseUnavailable is the one answer for a caused_by_run that is
	// missing, another team's, or not earlier, so it is no existence oracle.
	ErrRunCauseUnavailable = errors.New("caused_by_run does not name an earlier Run of this team")
	// ErrVersionedAdmissionUnavailable is the refusal while this server speaks
	// for no activation epoch, or the durable activation marker does not admit
	// its epoch. Nothing falls back to legacy admission; there is none.
	ErrVersionedAdmissionUnavailable = fmt.Errorf("versioned run admission is not activated on this server: %w", atc.ErrRunResultsUnavailable)
)

// AdmitVersionedRun admits one v2 Run of the referenced template inside tx,
// or replays the Run the same scoped invocation already committed. It is the
// port's only admission: the v2 HTTP route and the run_pipeline step both come
// here, and nothing creates legacy_v1 Runs any more.
//
// The order is the contract. The operator's hold and the activation epoch are
// properties of the server, answered before anything about the call; the key
// and the principal's shape are checked before any row is touched;
// authorization is decided against the reference's team before the template
// is resolved, so an unauthorized caller learns nothing about existence; and
// the scoped replay lookup happens only after all of that, in the factory.
//
// Every read goes through tx. The caller holds one pooled connection from
// Begin to commit, and a read on the pool here would want a second one while
// the first is checked out; atc/runs/connection_budget_test.go pins that.
func (a *admitter) AdmitVersionedRun(ctx context.Context, tx Tx, adm Admission, epoch int64) (Run, bool, error) {
	// Every admission obeys the operator's hold before validating the request
	// or touching its transaction, including in-process callers.
	if !atc.EnablePipelineRunCreation {
		return Run{}, false, atc.ErrPipelineRunCreationDisabled
	}
	// A server that speaks for no activation epoch cannot admit a v2 Run at
	// all, whatever the call says.
	if epoch <= 0 {
		return Run{}, false, ErrVersionedAdmissionUnavailable
	}
	if !validInvocationKey(adm.ContractKey) {
		return Run{}, false, ErrInvalidInvocationKey
	}
	if adm.Principal.Claims != nil {
		if subject, ok := adm.Principal.Claims["sub"].(string); !ok || subject == "" {
			return Run{}, false, ErrUnauthorized
		}
	}
	if adm.Correlation != "" && !atc.ValidRunInvocationToken(adm.Correlation) {
		return Run{}, false, ErrInvalidCorrelation
	}
	// The one place the port bridges its own interface back to the concrete
	// transaction type; a foreign Tx is refused rather than panicking.
	dbTx, ok := tx.(db.Tx)
	if !ok {
		return Run{}, false, ForeignTransactionError{}
	}
	auth, err := a.authorizeActionLocked(ctx, dbTx, adm.Template.Team, adm.Principal, atc.CreatePipelineRunV2)
	if err != nil {
		return Run{}, false, err
	}
	pipeline, err := a.resolveTemplate(dbTx, auth, adm.Template)
	if err != nil {
		return Run{}, false, err
	}
	// After the template is resolved, because the check compares it with the
	// calling build's own pipeline, and before anything is created.
	if err := refuseDirectRecursion(auth, pipeline); err != nil {
		return Run{}, false, err
	}
	// The cause is authorized with the template: it must be a Run of the same
	// team, which the principal was just authorized on. The factory resolves it
	// under the creation prefix and refuses anything else without saying why.
	opts := db.RunCreationOpts{ActivationEpoch: epoch, Inputs: adm.Inputs, SealedInputAuthority: a.sealedInputs, CausedByRun: adm.CausedByRun, Correlation: adm.Correlation, Invocation: &db.RunInvocationIdentity{
		PrincipalDigest: principalDigest(adm.Principal, auth),
		KeyDigest:       invocationDigest("key", adm.ContractKey),
	}}
	return a.createAdmission(ctx, dbTx, pipeline, adm, auth.createdBy, opts)
}

// principalDigest is the principal part of an invocation's server-owned scope.
//
// A person is their verified token subject. A build is its team plus the
// pipeline it runs in, both taken from the verified builds row rather than from
// the principal's own assertions. Every build of one pipeline therefore shares
// a scope: a parked step resumed after a web restart presents the same
// (build_id, plan_id) key and finds the Run it already admitted, while another
// pipeline on the team presenting the same key does not. A one-off build has no
// pipeline and scopes to its team alone. The leading NUL keeps a build scope
// from ever equalling a token subject.
func principalDigest(principal Principal, auth authorization) string {
	if auth.caller != nil {
		return runinput.PrincipalDigest(fmt.Sprintf("\x00build/team/%d/pipeline/%d", auth.team.ID(), auth.caller.pipelineID))
	}
	subject, _ := principal.Claims["sub"].(string)
	return runinput.PrincipalDigest(subject)
}

func invocationDigest(kind, value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("run-invocation-"+kind+"/v1\x00"+value)))
}

func validInvocationKey(key string) bool { return atc.ValidRunInvocationToken(key) }

func (a *admitter) authorizeActionLocked(ctx context.Context, dbTx db.Tx, team string, principal Principal, action string) (authorization, error) {
	// Authorization and its supporting team rows stay current through commit.
	// Admin membership is also a source of authority, so lock those rows in the
	// same sorted prefix before consulting the ordinary accessor.
	rows, err := dbTx.QueryContext(ctx, `SELECT id FROM teams WHERE lower(name)=lower($1) OR admin ORDER BY id FOR SHARE`, team)
	if err != nil {
		return authorization{}, err
	}
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return authorization{}, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return authorization{}, err
	}
	return a.authorizeAction(dbTx, team, principal, action)
}
