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
)

func (a *admitter) AdmitVersionedRun(ctx context.Context, tx Tx, adm Admission, epoch int64) (Run, bool, error) {
	// Every admission port obeys the operator's hold before validating the
	// request or touching its transaction, including in-process callers.
	if !atc.EnablePipelineRunCreation {
		return Run{}, false, atc.ErrPipelineRunCreationDisabled
	}
	if !validInvocationKey(adm.ContractKey) {
		return Run{}, false, ErrInvalidInvocationKey
	}
	subject, ok := adm.Principal.Claims["sub"].(string)
	if !ok || subject == "" {
		return Run{}, false, ErrUnauthorized
	}
	if adm.Correlation != "" && !atc.ValidRunInvocationToken(adm.Correlation) {
		return Run{}, false, ErrInvalidCorrelation
	}
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
	// The cause is authorized with the template: it must be a Run of the same
	// team, which the principal was just authorized on. The factory resolves it
	// under the creation prefix and refuses anything else without saying why.
	opts := db.RunCreationOpts{ActivationEpoch: epoch, Inputs: adm.Inputs, SealedInputAuthority: a.sealedInputs, CausedByRun: adm.CausedByRun, Correlation: adm.Correlation, Invocation: &db.RunInvocationIdentity{
		PrincipalDigest: runinput.PrincipalDigest(subject),
		KeyDigest:       invocationDigest("key", adm.ContractKey),
	}}
	return a.createAdmission(ctx, dbTx, pipeline, adm, auth.createdBy, opts)
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
