package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

type pendingRunInput struct {
	name        string
	audience    runinput.Audience
	authority   *runinput.Authority
	source      atc.RunInputSource
	binding     atc.RunInputBinding
	sourceClaim output.ClaimID
}

func resolveRunInputs(ctx context.Context, tx Tx, audience runinput.Audience, declarations []atc.RunTaskDeclaration, supplied map[string]atc.RunInputSource, authority *runinput.Authority) ([]pendingRunInput, error) {
	names := map[string]bool{}
	for _, task := range declarations {
		for _, input := range task.Inputs {
			names[input.Name] = true
		}
	}
	if len(names) != len(supplied) {
		return nil, atc.ErrInvalidRunInputs
	}
	ordered := make([]string, 0, len(names))
	for name := range supplied {
		if !names[name] {
			return nil, atc.ErrInvalidRunInputs
		}
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	inputs := make([]pendingRunInput, 0, len(ordered))
	for _, name := range ordered {
		source := supplied[name]
		audience.Input = name
		binding, err := resolveRunInputSource(ctx, tx, audience, source, authority)
		if err != nil {
			return nil, err
		}
		inputs = append(inputs, pendingRunInput{name: name, audience: audience, authority: authority, source: source, sourceClaim: binding.ClaimID, binding: atc.RunInputBinding{Source: source.WithoutBearer(), Ref: binding.Ref, ClaimID: output.ClaimID(uuid.NewString()), Epoch: audience.Epoch}})
	}
	return inputs, nil
}

// The caller holds the shared team prefix. Prior results require that team's
// membership; sealed sources require the complete authenticated audience.
// No source Run lock is acquired after the new Run's creation prefix. Exact
// publication availability is rechecked by AcquireClaim under the Hangar locks.
func resolveRunInputSource(ctx context.Context, tx Tx, audience runinput.Audience, source atc.RunInputSource, authority *runinput.Authority) (atc.RunResultBinding, error) {
	if err := source.Validate(); err != nil {
		return atc.RunResultBinding{}, err
	}
	if source.SourceID != "" {
		ref, err := authority.Verify(source.SourceID, source.Bearer, audience)
		if err != nil {
			return atc.RunResultBinding{}, atc.ErrRunInputUnavailable
		}
		return atc.RunResultBinding{Ref: ref}, nil
	}
	// The result must have been published under the Hangar epoch this admission
	// binds under. A Run's own activation epoch says nothing about it. After a
	// Hangar rotation an earlier Run's result is therefore unavailable as an
	// input -- a limit the owner accepted (M-2 decision 3), not an oversight.
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT r.result_manifest->$2 FROM pipeline_runs r
		JOIN pipelines p ON p.id=r.template_pipeline_id
		WHERE r.id=$1 AND p.team_id=$3 AND r.status='succeeded'
		AND EXISTS (SELECT 1 FROM hangar_claims c
			WHERE c.claim_id::text = r.result_manifest->$2->>'claim_id' AND c.activation_epoch=$4)`, source.RunID, source.Result, audience.TeamID, audience.Epoch).Scan(&body)
	if err == sql.ErrNoRows {
		return atc.RunResultBinding{}, atc.ErrRunInputUnavailable
	}
	if err != nil {
		return atc.RunResultBinding{}, err
	}
	var binding atc.RunResultBinding
	if len(body) == 0 || json.Unmarshal(body, &binding) != nil || binding.Ref.Validate() != nil || binding.ClaimID.Validate() != nil {
		return atc.RunResultBinding{}, atc.ErrRunInputUnavailable
	}
	return binding, nil
}

func retainRunInputs(ctx context.Context, tx Tx, runID int, inputs []pendingRunInput) error {
	if len(inputs) == 0 {
		return nil
	}
	prefix, err := HangarConsumerPrefixHeld("pipeline-run-input-admission")
	if err != nil {
		return err
	}
	repository := NewHangarOutputRepository(prefix)
	locks := HangarLockRequest{}
	for _, input := range inputs {
		ref := input.binding.Ref
		locks.Logical = append(locks.Logical, HangarLogicalKey{Scope: ref.Scope, Digest: ref.Digest})
		locks.Exact = append(locks.Exact, ref)
		locks.Claims = append(locks.Claims, input.binding.ClaimID)
		if input.sourceClaim != "" {
			locks.Claims = append(locks.Claims, input.sourceClaim)
		}
	}
	if _, err = LockHangarSuffix(ctx, tx, prefix, locks); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	for _, input := range inputs {
		binding := input.binding
		current, err := resolveRunInputSource(ctx, tx, input.audience, input.source, input.authority)
		if err != nil {
			return err
		}
		if current.Ref != binding.Ref || current.ClaimID != input.sourceClaim {
			return atc.ErrRunInputUnavailable
		}
		if input.sourceClaim != "" {
			claims, err := repository.ReadClaims(ctx, tx, binding.Ref)
			if err != nil {
				return err
			}
			protected := false
			for _, claim := range claims {
				if claim.ClaimID == input.sourceClaim && claim.Ref == binding.Ref && claim.Active() {
					protected = true
					break
				}
			}
			if !protected {
				return atc.ErrRunInputUnavailable
			}
		}
		if err = repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{ProtocolVersion: output.ProtocolVersion, ClaimID: binding.ClaimID, Ref: binding.Ref, ConsumerBindingID: output.OpaqueID(binding.ClaimID), RequestedAt: output.NewTimestamp(now)}); err != nil {
			if binding.Source.SourceID != "" {
				return atc.ErrRunInputUnavailable
			}
			return err
		}
		var sourceRunID, sourceResult, sourceID any
		if binding.Source.SourceID != "" {
			sourceID = binding.Source.SourceID
		} else {
			sourceRunID, sourceResult = binding.Source.RunID, binding.Source.Result
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_inputs
			(run_id,name,source_run_id,source_result,scope,digest,generation,claim_id,activation_epoch,source_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, runID, input.name, sourceRunID, sourceResult, string(binding.Ref.Scope), string(binding.Ref.Digest), binding.Ref.Generation, string(binding.ClaimID), binding.Epoch, sourceID)
		if err != nil {
			return err
		}
	}
	return nil
}

func readRunInputs(ctx context.Context, tx Tx, runID int) (map[string]atc.RunInputBinding, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name,coalesce(source_run_id,0),coalesce(source_result,''),scope,digest,generation,claim_id,activation_epoch,coalesce(source_id,'') FROM pipeline_run_inputs WHERE run_id=$1 ORDER BY name`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inputs := map[string]atc.RunInputBinding{}
	for rows.Next() {
		var name string
		var binding atc.RunInputBinding
		if err = rows.Scan(&name, &binding.Source.RunID, &binding.Source.Result, &binding.Ref.Scope, &binding.Ref.Digest, &binding.Ref.Generation, &binding.ClaimID, &binding.Epoch, &binding.Source.SourceID); err != nil {
			return nil, err
		}
		inputs[name] = binding
	}
	return inputs, rows.Err()
}
