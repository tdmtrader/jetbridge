package db

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// teamRunIDs selects every Run whose template belongs to the team.
const teamRunIDs = `SELECT run.id FROM pipeline_runs run
	JOIN pipelines template ON template.id = run.template_pipeline_id
	WHERE template.team_id = $1`

// purgeTeamRunEvidence removes every Run evidence row the team owns, children
// before parents, so the builds, payloads and headers that follow have nothing
// left referencing them.
//
// Evidence is otherwise immutable and undeletable; the store admits these
// deletes only while the caller's transaction carries the team purge marker.
// Hangar claims held by the team's Runs are released first, in this same
// transaction: a purged Run can never be read again, and a claim with no
// surviving owner would pin its tree forever. The released claim rows stay as
// Hangar's tombstones.
//
// The caller holds the team's template and Run locks and has set the marker.
// The Runs' builds are locked next, before any evidence goes, in the Run ->
// builds order every Run path uses. Check collection locks a check build and
// then deletes its executions; deleting executions first would deadlock
// with it.
func purgeTeamRunEvidence(ctx context.Context, tx Tx, teamID int) error {
	if _, err := tx.ExecContext(ctx, `SELECT id FROM builds WHERE pipeline_run_id IN (`+teamRunIDs+`) ORDER BY id FOR UPDATE`, teamID); err != nil {
		return err
	}
	if err := releaseTeamRunClaims(ctx, tx, teamID); err != nil {
		return err
	}
	executions := `SELECT execution_id, execution_fence FROM pipeline_run_executions WHERE run_id IN (` + teamRunIDs + `)`
	handoffs := `SELECT handoff_id FROM pipeline_run_output_starts WHERE run_id IN (` + teamRunIDs + `)`
	for _, statement := range []string{
		`DELETE FROM pipeline_run_input_uploads
		 WHERE team_id = $1 OR template_pipeline_id IN (SELECT id FROM pipelines WHERE team_id = $1)`,
		`DELETE FROM pipeline_run_credential_handoffs WHERE run_id IN (` + teamRunIDs + `)`,
		`DELETE FROM pipeline_run_cancellation_cursors WHERE run_id IN (` + teamRunIDs + `)`,
		`DELETE FROM pipeline_run_cancellation_operations WHERE run_id IN (` + teamRunIDs + `)`,
		`DELETE FROM pipeline_run_cancellation_progress WHERE run_id IN (` + teamRunIDs + `)`,
		`DELETE FROM pipeline_run_execution_starts WHERE (execution_id, execution_fence) IN (` + executions + `)`,
		`DELETE FROM pipeline_run_execution_closures WHERE (execution_id, execution_fence) IN (` + executions + `)`,
		`DELETE FROM pipeline_run_executions WHERE run_id IN (` + teamRunIDs + `)`,
		`DELETE FROM pipeline_run_output_candidates WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_discards WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_releases WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_finishes WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_holds WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_cancellation_evidence WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_cancellation_classifications WHERE handoff_id IN (` + handoffs + `)`,
		`DELETE FROM pipeline_run_output_starts WHERE run_id IN (` + teamRunIDs + `)`,
		`DELETE FROM pipeline_run_inputs WHERE run_id IN (` + teamRunIDs + `)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, teamID); err != nil {
			return err
		}
	}
	return nil
}

// releaseTeamRunClaims releases every still-active Hangar claim a team's Runs
// hold: result candidates, bound inputs and input uploads. It takes the whole
// Hangar suffix once, in its one order, after the Run locks the caller holds.
func releaseTeamRunClaims(ctx context.Context, tx Tx, teamID int) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.claim_id, l.scope, l.digest, l.generation
		FROM hangar_claims c
		JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
		WHERE c.released_at IS NULL AND c.claim_id IN (
			SELECT candidate.claim_id FROM pipeline_run_output_candidates candidate
			JOIN pipeline_run_output_starts s USING (handoff_id)
			WHERE s.run_id IN (`+teamRunIDs+`)
			UNION
			SELECT claim_id FROM pipeline_run_inputs WHERE run_id IN (`+teamRunIDs+`)
			UNION
			SELECT claim_id FROM pipeline_run_input_uploads
			WHERE team_id = $1 OR template_pipeline_id IN (SELECT id FROM pipelines WHERE team_id = $1)
		)
		ORDER BY c.claim_id`, teamID)
	if err != nil {
		return err
	}
	type heldClaim struct {
		id  output.ClaimID
		ref hangar.TreeRef
	}
	var claims []heldClaim
	for rows.Next() {
		var claim heldClaim
		if err = rows.Scan(&claim.id, &claim.ref.Scope, &claim.ref.Digest, &claim.ref.Generation); err != nil {
			Close(rows)
			return err
		}
		claims = append(claims, claim)
	}
	err = rows.Err()
	Close(rows)
	if err != nil || len(claims) == 0 {
		return err
	}

	prefix, err := HangarConsumerPrefixHeld("pipeline-run-team-purge")
	if err != nil {
		return err
	}
	request := HangarLockRequest{}
	for _, claim := range claims {
		request.Logical = append(request.Logical, HangarLogicalKey{Scope: claim.ref.Scope, Digest: claim.ref.Digest})
		request.Exact = append(request.Exact, claim.ref)
		request.Claims = append(request.Claims, claim.id)
	}
	if _, err = LockHangarSuffix(ctx, tx, prefix, request); err != nil {
		return err
	}
	var now time.Time
	if err = tx.QueryRowContext(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	repository := NewHangarOutputRepository(prefix)
	for _, claim := range claims {
		if err = repository.ReleaseClaim(ctx, tx, output.ClaimRelease{
			ProtocolVersion: output.ProtocolVersion,
			ClaimID:         claim.id,
			Ref:             claim.ref,
			RequestedAt:     output.NewTimestamp(now.UTC()),
		}); err != nil {
			return fmt.Errorf("release Run claim %s: %w", claim.id, err)
		}
	}
	return nil
}
