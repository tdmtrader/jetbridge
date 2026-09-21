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

// Preserve explicit presence (including null), while using the retained schema
// to normalize scalar values. Applied defaults belong only to admitted facts.
func runCallerIntent(schema []atc.ParamSchema, supplied atc.RunParams, inputs map[string]atc.RunInputSource) ([]byte, error) {
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
		Params atc.RunParams                 `json:"params"`
		Inputs map[string]atc.RunInputSource `json:"inputs"`
	}{explicit, canonicalInputs})
}

func runInvocationDocumentDigest(kind string, document []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(append([]byte("run-invocation-"+kind+"/v1\x00"), document...)))
}

func (f *pipelineRunFactory) replayRunInvocation(ctx context.Context, tx Tx, template Pipeline, params atc.RunParams, inputs map[string]atc.RunInputSource, identity RunInvocationIdentity) (RunCreation, bool, error) {
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
	intent, err := runCallerIntent(definition.Template.Params, params, inputs)
	if err != nil || !bytes.Equal(intent, []byte(document)) {
		return RunCreation{}, false, ErrRunInvocationConflict
	}
	run, err := f.queryOneRun(tx, pipelineRunsQuery.Where(sq.Eq{"r.id": runID}))
	if err != nil {
		return RunCreation{}, false, err
	}
	if run == nil || run.ContractVersion() != atc.RunContractV2 {
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
	}{run.ID(), teamID, run.TemplatePipelineID(), run.ActivationEpoch(), templateDigest, run.ConfigHash(), run.Params(), inputs})
}
