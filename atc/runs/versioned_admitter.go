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
	// Causation is not silently discarded by this scalar admission checkpoint.
	if adm.CausedByRun != nil {
		return Run{}, false, ErrUnsupportedInvocation
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
	opts := db.RunCreationOpts{ActivationEpoch: epoch, Inputs: adm.Inputs, SealedInputAuthority: a.sealedInputs, Invocation: &db.RunInvocationIdentity{
		PrincipalDigest: runinput.PrincipalDigest(subject),
		KeyDigest:       invocationDigest("key", adm.ContractKey),
	}}
	return a.createAdmission(ctx, dbTx, pipeline, adm, auth.createdBy, opts)
}

func invocationDigest(kind, value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("run-invocation-"+kind+"/v1\x00"+value)))
}

func validInvocationKey(key string) bool {
	if len(key) < 1 || len(key) > 128 {
		return false
	}
	for _, c := range []byte(key) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-' {
			continue
		}
		return false
	}
	return true
}

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
