package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

var (
	ErrInvalidRunInvocation    = errors.New("invalid versioned Run invocation")
	ErrRunInvocationConflict   = errors.New("Run invocation conflict")
	runInvocationDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

// RunInvocationIdentity contains only domain-separated hashes. Raw caller keys
// and authentication claims never reach the durable invocation record.
type RunInvocationIdentity struct {
	PrincipalDigest string
	KeyDigest       string
}

func (identity RunInvocationIdentity) valid() bool {
	return runInvocationDigestPattern.MatchString(identity.PrincipalDigest) && runInvocationDigestPattern.MatchString(identity.KeyDigest)
}

// runCaller is everything the caller states about one invocation. The
// caller-intent digest covers exactly these facts.
type runCaller struct {
	Params      atc.RunParams
	Inputs      map[string]atc.RunInputSource
	CausedByRun *int
	Correlation string
}

// ErrRunCauseUnavailable is one refusal for every caused_by_run that cannot be
// linked -- missing, another team's, or not earlier -- so the answer never
// discloses whether some other team's Run exists.
var ErrRunCauseUnavailable = errors.New("caused_by_run does not name an earlier Run of this team")

// resolveRunCause runs under the creation prefix's team FOR SHARE, which is
// what makes one read enough: a predecessor Run leaves only through its team's
// purge, and that waits for this transaction. It takes no Run lock, so creation
// never reaches into the post-creation prefix.
func resolveRunCause(ctx context.Context, tx Tx, teamID int, cause *int) error {
	if cause == nil {
		return nil
	}
	if *cause <= 0 {
		return ErrRunCauseUnavailable
	}
	var linked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pipeline_runs r JOIN pipelines t ON t.id = r.template_pipeline_id
		WHERE r.id = $1 AND t.team_id = $2)`, *cause, teamID).Scan(&linked)
	if err != nil {
		return err
	}
	if !linked {
		return ErrRunCauseUnavailable
	}
	return nil
}

// Preserve explicit presence (including null), while using the retained schema
// to normalize scalar values. Applied defaults belong only to admitted facts.
// Cause and correlation are omitted when absent so records retained before
// they existed canonicalize to the same bytes.
func runCallerIntent(schema []atc.ParamSchema, caller runCaller) ([]byte, error) {
	supplied, inputs := caller.Params, caller.Inputs
	if len(inputs) > 64 {
		return nil, atc.ErrInvalidRunInputs
	}
	if inputs == nil {
		inputs = map[string]atc.RunInputSource{}
	}
	canonicalInputs := make(map[string]atc.RunInputSource, len(inputs))
	for name, source := range inputs {
		source = source.WithoutBearer()
		if err := source.Validate(); err != nil {
			return nil, err
		}
		canonicalInputs[name] = source
	}
	for _, reserved := range []string{"run", "run_id"} {
		if _, exists := supplied[reserved]; exists {
			return nil, atc.InvalidRunParamsError{Err: errors.New("reserved Run parameter supplied")}
		}
	}
	normalized, err := atc.ValidateRunParams(schema, supplied)
	if err != nil {
		return nil, err
	}
	explicit := atc.RunParams{}
	for name, value := range supplied {
		if value == nil {
			explicit[name] = nil
		} else {
			explicit[name] = normalized[name]
		}
	}
	return json.Marshal(struct {
		Params      atc.RunParams                 `json:"params"`
		Inputs      map[string]atc.RunInputSource `json:"inputs"`
		CausedByRun *int                          `json:"caused_by_run,omitempty"`
		Correlation string                        `json:"correlation,omitempty"`
	}{explicit, canonicalInputs, caller.CausedByRun, caller.Correlation})
}

func runInvocationDocumentDigest(kind string, document []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte("run-invocation-"+kind+"/v1\x00"), document...)))
}

func (f *pipelineRunFactory) replayRunInvocation(ctx context.Context, tx Tx, template Pipeline, caller runCaller, identity RunInvocationIdentity) (RunCreation, bool, error) {
	var runID int
	var document, digest, admittedDigest, templateDigest string
	err := tx.QueryRowContext(ctx, `SELECT i.run_id, i.caller_document, i.caller_digest, i.admitted_digest, d.template_digest
		FROM pipeline_run_invocations i JOIN pipeline_run_definitions d ON d.run_id=i.run_id
		WHERE i.team_id=$1 AND i.template_pipeline_id=$2 AND i.principal_digest=$3 AND i.key_digest=$4`, template.TeamID(), template.ID(), identity.PrincipalDigest, identity.KeyDigest).Scan(&runID, &document, &digest, &admittedDigest, &templateDigest)
	if err == sql.ErrNoRows {
		return RunCreation{}, false, nil
	}
	if err != nil {
		return RunCreation{}, false, err
	}
	if runInvocationDocumentDigest("caller", []byte(document)) != digest {
		return RunCreation{}, false, errors.New("retained invocation attestation mismatch")
	}
	definition, found, err := readRunDefinition(tx, runID)
	if err != nil {
		return RunCreation{}, false, err
	}
	if !found {
		return RunCreation{}, false, errors.New("retained invocation definition missing")
	}
	intent, err := runCallerIntent(definition.Template.Params, caller)
	if err != nil || !bytes.Equal(intent, []byte(document)) {
		return RunCreation{}, false, ErrRunInvocationConflict
	}
	run, err := f.queryOneRun(tx, pipelineRunsQuery.Where(sq.Eq{"r.id": runID}))
	if err != nil {
		return RunCreation{}, false, err
	}
	if run == nil {
		return RunCreation{}, false, errors.New("retained invocation Run missing")
	}
	admitted, err := runAdmittedInvocation(ctx, tx, template.TeamID(), run, templateDigest)
	if err != nil {
		return RunCreation{}, false, err
	}
	if runInvocationDocumentDigest("admitted", admitted) != admittedDigest {
		return RunCreation{}, false, errors.New("retained admitted invocation attestation mismatch")
	}
	return RunCreation{Run: run, Replayed: true, Config: definition.Materialized, ConfigHash: run.ConfigHash()}, true, nil
}

func retainRunInvocation(ctx context.Context, tx Tx, teamID int, creation RunCreation, template atc.Config, intent []byte, identity RunInvocationIdentity) error {
	templateJSON, err := json.Marshal(template)
	if err != nil {
		return err
	}
	admitted, err := runAdmittedInvocation(ctx, tx, teamID, creation.Run, templateDefinitionDigest(templateJSON))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_invocations
		(run_id,team_id,template_pipeline_id,principal_digest,key_digest,caller_document,caller_digest,admitted_digest)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, creation.Run.ID(), teamID, creation.Run.TemplatePipelineID(), identity.PrincipalDigest, identity.KeyDigest, string(intent), runInvocationDocumentDigest("caller", intent), runInvocationDocumentDigest("admitted", admitted))
	return err
}

func runAdmittedInvocation(ctx context.Context, tx Tx, teamID int, run PipelineRun, templateDigest string) ([]byte, error) {
	inputs, err := readRunInputs(ctx, tx, run.ID())
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		RunID          int                            `json:"run_id"`
		TeamID         int                            `json:"team_id"`
		TemplateID     int                            `json:"template_id"`
		Epoch          int64                          `json:"epoch"`
		TemplateDigest string                         `json:"template_digest"`
		ConfigDigest   string                         `json:"config_digest"`
		Params         atc.Params                     `json:"params"`
		Inputs         map[string]atc.RunInputBinding `json:"inputs"`
		CausedByRun    *int                           `json:"caused_by_run,omitempty"`
		Correlation    string                         `json:"correlation,omitempty"`
	}{run.ID(), teamID, run.TemplatePipelineID(), run.ActivationEpoch(), templateDigest, run.ConfigHash(), run.Params(), inputs, run.CausedByRun(), run.Correlation()})
}
