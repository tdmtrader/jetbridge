package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// The retained observation and public wire result share one declaration.
type RunResultBinding = atc.RunResultBinding
type RunTerminalResult = atc.RunTerminalResult

func (f *pipelineRunFactory) TerminalResult(ctx context.Context, runID int) (RunTerminalResult, bool, error) {
	var result RunTerminalResult
	var body []byte
	err := f.conn.QueryRowContext(ctx, `SELECT status,completed_at,result_manifest,terminal_observation_version FROM pipeline_runs
 WHERE id=$1 AND run_contract_version='v2' AND status<>'running'`, runID).Scan(&result.Status, &result.CompletedAt, &body, &result.Version)
	if err == sql.ErrNoRows {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if err := json.Unmarshal(body, &result.Results); err != nil {
		return result, false, err
	}
	return result, true, nil
}

func (f *pipelineRunFactory) AfterRunCompleted() { announceRunCompletion(f.conn.Bus()) }

func (f *pipelineRunFactory) PendingOutputRuns(ctx context.Context, tx Tx, afterID, limit int) ([]int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM pipeline_runs WHERE run_contract_version='v2' AND status='running' AND id>$1 ORDER BY id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FinalizeOutputRun is called in a fresh transaction. All producer and scheduler
// mutations share its Run lock. It takes the entire Hangar suffix once before
// selecting or releasing claims, and makes no network calls.
func (f *pipelineRunFactory) FinalizeOutputRun(ctx context.Context, tx Tx, runID int) (bool, error) {
	return f.finalizeOutputRun(ctx, tx, runID, nil)
}

func (f *pipelineRunFactory) finalizeOutputRun(ctx context.Context, tx Tx, runID int, cancellation *runCancellationCommit) (bool, error) {
	run, err := lockRunResultPublication(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	if run.ContractVersion() != atc.RunContractV2 || run.Status() != atc.RunStatusRunning {
		if cancellation != nil && run.ContractVersion() == atc.RunContractV2 && run.Status() == atc.RunStatusAborted && run.CancellationRequested() {
			return true, cancellation.check(ctx, tx)
		}
		return false, nil
	}
	// Only a current cancellation operation may publish after the fence. The
	// ordinary result component leaves accepted cancellation to its worker.
	if run.CancellationRequested() && cancellation == nil {
		return false, nil
	}
	if cancellation != nil && !run.CancellationRequested() {
		return false, ErrRunCancellationProgressStale
	}
	payload, found := run.InstancePipelineID()
	if !found {
		return false, ErrPipelineRunPayloadGone
	}
	var completion runCompletionState
	var ready bool
	if cancellation != nil {
		ready, err = inspectRunCancellationQuiescence(ctx, tx, runID, payload)
		completion.Status = atc.RunStatusAborted
	} else {
		completion, ready, err = inspectRunCompletion(tx, runID, payload)
	}
	if err != nil || !ready {
		return false, err
	}
	var pending bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_output_starts WHERE run_id=$1 AND NOT run_output_closed(handoff_id))`, runID).Scan(&pending); err != nil {
		return false, err
	}
	if pending {
		return false, nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM builds b
 WHERE b.pipeline_run_id=$1 AND NOT run_execution_closed(b.id))`, runID).Scan(&pending); err != nil {
		return false, err
	}
	if pending {
		return false, nil
	}
	definition, found, err := readRunDefinition(tx, runID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("Run %d has no retained definition", runID)
	}
	declarations, err := atc.RunTaskDeclarations(definition.Materialized)
	if err != nil {
		return false, err
	}
	candidates, err := readRunCandidates(ctx, tx, runID)
	if err != nil {
		return false, err
	}
	repository, err := lockRunCandidateClaims(ctx, tx, candidates)
	if err != nil {
		return false, err
	}
	results := map[string]RunResultBinding{}
	if completion.Status == atc.RunStatusSucceeded {
		for _, d := range declarations {
			if d.Result == nil {
				continue
			}
			build, ok := completion.Builds[d.JobName]
			selected := false
			if ok && build.Status == BuildStatusSucceeded {
				for _, c := range candidates {
					if c.BuildID == build.ID && c.TaskID == d.TaskID && c.Name == d.Result.Name {
						results[d.Result.Name] = c.RunResultBinding
						selected = true
						break
					}
				}
			}
			if !selected {
				completion.Status = atc.RunStatusErrored
				results = map[string]RunResultBinding{}
				break
			}
		}
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT now()`).Scan(&now); err != nil {
		return false, err
	}
	now = now.UTC()
	selected := map[output.ClaimID]bool{}
	for _, r := range results {
		selected[r.ClaimID] = true
	}
	for _, c := range candidates {
		if !selected[c.ClaimID] {
			if err := repository.ReleaseClaim(ctx, tx, output.ClaimRelease{ProtocolVersion: output.ProtocolVersion, ClaimID: c.ClaimID, Ref: c.Ref, RequestedAt: output.NewTimestamp(now)}); err != nil {
				return false, err
			}
		}
	}
	body, err := json.Marshal(results)
	if err != nil {
		return false, err
	}
	observation := RunTerminalResult{Status: completion.Status, CompletedAt: now, Results: results}
	// Separate immutable identity from observation content. Neither the current
	// template config nor a mutable run-list/payload counter participates.
	content, err := json.Marshal(struct {
		BaseID      int               `json:"base_id"`
		Number      int               `json:"number"`
		Observation RunTerminalResult `json:"observation"`
	}{run.TemplatePipelineID(), run.Number(), observation})
	if err != nil {
		return false, err
	}
	version := fmt.Sprintf("run-terminal-v1:%x", sha256.Sum256(append([]byte("jetbridge/run-terminal/v1\x00"), content...)))
	if cancellation != nil {
		if err := cancellation.check(ctx, tx); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pipeline_runs SET status=$2,completed_at=$3,result_manifest=$4,terminal_observation_version=$5 WHERE id=$1`, runID, completion.Status, now, body, version); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE pipelines SET paused=true,paused_at=now(),paused_by='run-completed' WHERE id=$1 AND paused=false`, payload)
	return err == nil, err
}

// Post-creation order excludes the base template lock. Reads of ownership only
// discover the prefix; the immutable epoch/identity are rechecked under Run lock.
func lockRunResultPublication(ctx context.Context, tx Tx, runID int) (*pipelineRun, error) {
	var teamID int
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT p.team_id,coalesce(r.activation_epoch,0) FROM pipeline_runs r JOIN pipelines p ON p.id=r.template_pipeline_id WHERE r.id=$1`, runID).Scan(&teamID, &epoch); err != nil {
		return nil, err
	}
	var id int
	if err := tx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 FOR SHARE`, teamID).Scan(&id); err != nil {
		return nil, err
	}
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT epoch FROM pipeline_run_activation WHERE singleton FOR SHARE`).Scan(&current); err != nil {
		return nil, err
	}
	if epoch > 0 {
		ready, err := hangarLockRecoverableEpoch(ctx, tx, epoch)
		if err != nil {
			return nil, err
		}
		if current < epoch || !ready {
			return nil, atc.ErrRunResultsUnavailable
		}
	}
	run := &pipelineRun{}
	if err := scanPipelineRun(run, pipelineRunsQuery.Where(sq.Eq{"r.id": runID}).Suffix("FOR NO KEY UPDATE OF r").RunWith(tx).QueryRowContext(ctx)); err != nil {
		return nil, err
	}
	if run.ActivationEpoch() != epoch {
		return nil, atc.ErrRunResultsUnavailable
	}
	if run.Status() != atc.RunStatusRunning || run.ContractVersion() != atc.RunContractV2 {
		return run, nil
	}
	payload, found := run.InstancePipelineID()
	if !found {
		return nil, ErrPipelineRunPayloadGone
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM pipelines WHERE id=$1 FOR NO KEY UPDATE`, payload).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM jobs WHERE pipeline_id=$1 ORDER BY id FOR UPDATE`, payload); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT id FROM builds WHERE pipeline_run_id=$1 ORDER BY id FOR UPDATE`, runID); err != nil {
		return nil, err
	}
	return run, nil
}

type runCandidate struct {
	RunResultBinding
	BuildID      int
	TaskID, Name string
	Handoff      output.HandoffID
	Reservation  output.ReservationID
}

func readRunCandidates(ctx context.Context, tx Tx, runID int) ([]runCandidate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.build_id,s.task_id,s.result_name,s.handoff_id,f.reservation_id,c.scope,c.digest,c.generation,c.claim_id
 FROM pipeline_run_output_starts s JOIN pipeline_run_output_candidates c USING(handoff_id) JOIN pipeline_run_output_finishes f USING(handoff_id)
 WHERE s.run_id=$1 ORDER BY s.handoff_id`, runID)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var out []runCandidate
	for rows.Next() {
		var c runCandidate
		if err := rows.Scan(&c.BuildID, &c.TaskID, &c.Name, &c.Handoff, &c.Reservation, &c.Ref.Scope, &c.Ref.Digest, &c.Ref.Generation, &c.ClaimID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Take the complete Hangar suffix once and verify every candidate claim.
// Cancellation settlement and terminal publication use the same claim proof.
func lockRunCandidateClaims(ctx context.Context, tx Tx, candidates []runCandidate) (*HangarOutputRepository, error) {
	repository := runOutputRepository()
	request := HangarLockRequest{}
	for _, c := range candidates {
		request.Logical = append(request.Logical, HangarLogicalKey{Scope: c.Ref.Scope, Digest: c.Ref.Digest})
		request.Exact = append(request.Exact, c.Ref)
		request.Captures = append(request.Captures, c.Reservation)
		request.Receipts = append(request.Receipts, c.Reservation)
		request.Claims = append(request.Claims, c.ClaimID)
	}
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, request); err != nil {
		return nil, err
	}
	// A candidate whose claim was released outside the Run boundary is corrupt
	// evidence, not permission to publish a different generation.
	claims := map[hangar.TreeRef][]output.ClaimRecord{}
	for _, c := range candidates {
		if _, ok := claims[c.Ref]; !ok {
			var err error
			claims[c.Ref], err = repository.ReadClaims(ctx, tx, c.Ref)
			if err != nil {
				return nil, err
			}
		}
		protected := false
		for _, claim := range claims[c.Ref] {
			if claim.ClaimID == c.ClaimID && claim.Active() && claim.ConsumerBindingID == output.OpaqueID(c.Handoff) {
				protected = true
				break
			}
		}
		if !protected {
			return nil, fmt.Errorf("%w: Run candidate has no active matching claim", output.ErrIncomplete)
		}
	}
	return repository, nil
}
