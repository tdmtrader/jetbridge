package db

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// runPurgeScope names the Runs one purge of Run evidence removes, by their
// owner: a team (team delete) or one template (a template destroyed with its
// Run history). Both statements are bound to the owner's id as $1.
type runPurgeScope struct {
	// consumer names the purge as the Hangar consumer releasing its claims.
	consumer string
	// runIDs selects the id of every Run in scope.
	runIDs string
	// uploads is a predicate on pipeline_run_input_uploads selecting the
	// input uploads in scope. An upload belongs to a template, not yet to
	// any Run.
	uploads string
}

// teamRunPurge selects every Run whose template belongs to the team, and
// every input upload made on the team.
var teamRunPurge = runPurgeScope{
	consumer: "pipeline-run-team-purge",
	runIDs: `SELECT run.id FROM pipeline_runs run
	JOIN pipelines template ON template.id = run.template_pipeline_id
	WHERE template.team_id = $1`,
	uploads: `team_id = $1 OR template_pipeline_id IN (SELECT id FROM pipelines WHERE team_id = $1)`,
}

// templateRunPurge selects every Run of one template, and every input upload
// made for it.
var templateRunPurge = runPurgeScope{
	consumer: "pipeline-run-template-purge",
	runIDs:   `SELECT id FROM pipeline_runs WHERE template_pipeline_id = $1`,
	uploads:  `template_pipeline_id = $1`,
}

// runEvidencePurgeMarker sets the transaction-scoped marker under which the
// evidence tables' triggers (run_team_purge_active() in the migrations) admit
// deletes. Despite its name it marks either purge of Run evidence by its
// owner: a team delete, or a template destroyed with its Run history. The
// triggers do not know which; the caller's scope bounds what is deleted. It
// never outlives the transaction that sets it.
const runEvidencePurgeMarker = `SELECT set_config('concourse.pipeline_run_team_purge', 'on', true)`

// purgeRunEvidence removes every Run evidence row of the Runs in scope,
// children before parents, so the builds, payloads and headers that follow
// have nothing left referencing them.
//
// Evidence is otherwise immutable and undeletable; the store admits these
// deletes only while the caller's transaction carries the purge marker.
// Hangar claims held by the Runs in scope are released first, in this same
// transaction: a purged Run can never be read again, and a claim with no
// surviving owner would pin its tree forever. The released claim rows stay as
// Hangar's tombstones.
//
// The caller holds the team, then the scope's template and Run locks, and has
// set the marker. The Runs' builds are locked next, before any evidence goes,
// in the Run -> builds order every Run path uses. Check collection locks a
// check build and then deletes its executions; deleting executions first
// would deadlock with it.
func purgeRunEvidence(ctx context.Context, tx Tx, scope runPurgeScope, ownerID int) error {
	runIDs := scope.runIDs
	if _, err := tx.ExecContext(ctx, `SELECT id FROM builds WHERE pipeline_run_id IN (`+runIDs+`) ORDER BY id FOR UPDATE`, ownerID); err != nil {
		return err
	}
	if err := releaseRunClaims(ctx, tx, scope, ownerID); err != nil {
		return err
	}
	executions := `SELECT execution_id, execution_fence FROM pipeline_run_executions WHERE run_id IN (` + runIDs + `)`
	for _, statement := range []string{
		`DELETE FROM pipeline_run_input_uploads WHERE ` + scope.uploads,
		`DELETE FROM pipeline_run_credential_handoffs WHERE run_id IN (` + runIDs + `)`,
		`DELETE FROM pipeline_run_cancellation_cursors WHERE run_id IN (` + runIDs + `)`,
		`DELETE FROM pipeline_run_cancellation_operations WHERE run_id IN (` + runIDs + `)`,
		`DELETE FROM pipeline_run_cancellation_progress WHERE run_id IN (` + runIDs + `)`,
		`DELETE FROM pipeline_run_execution_starts WHERE (execution_id, execution_fence) IN (` + executions + `)`,
		`DELETE FROM pipeline_run_execution_closures WHERE (execution_id, execution_fence) IN (` + executions + `)`,
		`DELETE FROM pipeline_run_executions WHERE run_id IN (` + runIDs + `)`,
		// The Run's link only. The capture row is Hangar's: its node marker
		// is released by the capture coordinator whether or not a Run still
		// names it.
		`DELETE FROM pipeline_run_captures WHERE run_id IN (` + runIDs + `)`,
		`DELETE FROM pipeline_run_inputs WHERE run_id IN (` + runIDs + `)`,
	} {
		if _, err := tx.ExecContext(ctx, statement, ownerID); err != nil {
			return err
		}
	}
	return nil
}

// releaseRunClaims releases every still-active Hangar claim the Runs in scope
// hold: result candidates, bound inputs and input uploads. It takes the whole
// Hangar suffix once, in its one order, after the Run locks the caller holds.
func releaseRunClaims(ctx context.Context, tx Tx, scope runPurgeScope, ownerID int) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.claim_id, l.scope, l.digest, l.generation
		FROM hangar_claims c
		JOIN hangar_exact_lifecycles l ON l.id = c.lifecycle_id
		WHERE c.released_at IS NULL AND c.claim_id IN (
			SELECT capture_claim.claim_id FROM pipeline_run_captures s
			JOIN hangar_claims capture_claim
			  ON capture_claim.consumer_binding_id = 'capture:' || s.execution_id::text || '/' || s.output_name
			WHERE s.run_id IN (`+scope.runIDs+`)
			UNION
			SELECT claim_id FROM pipeline_run_inputs WHERE run_id IN (`+scope.runIDs+`)
			UNION
			SELECT claim_id FROM pipeline_run_input_uploads WHERE `+scope.uploads+`
		)
		ORDER BY c.claim_id`, ownerID)
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

	prefix, err := HangarConsumerPrefixHeld(scope.consumer)
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

// activeTemplateRun reports whether any Run of the template is still being
// worked on, which refuses destroying the template with its Run history. A Run
// is active while it is running (which includes a requested cancellation that
// has not yet completed the Run as aborted, and an open build closure), or
// while the cancellation worker still holds one of its cancellation operations
// within that operation's term: publication of the aborted status may commit
// before the worker records the terminal operation done. An operation whose
// term has run out can no longer be completed by anyone once the Run has left
// running, so it does not hold the Run active forever.
const activeTemplateRun = `SELECT EXISTS (
	SELECT 1 FROM pipeline_runs run
	WHERE run.template_pipeline_id = $1 AND (
		run.status = 'running' OR EXISTS (
			SELECT 1 FROM pipeline_run_cancellation_operations op
			WHERE op.run_id = run.id AND op.completed_at IS NULL
			  AND op.claimed AND op.next_at > clock_timestamp())))`

// pendingTemplateLanding reports whether a landing queue entry that has not
// settled still depends on a Run of the template: the entry names the Run, or
// owns an intent that does. Deleting the Run would null out or cascade away
// the links the queue is still driving. A settled entry (landed or ejected,
// both final) only loses its history link, which the purge accepts.
//
// It is a plain read, not a lock. The landing engine's compose locks an entry
// before it admits a Run (team, template), and its land admission locks the
// Run before the entry and intent, so no lock order on landing rows is safe
// against both. None is needed: a link to a Run is written only in the
// transaction that admits that Run, which needs the template lock this purge
// already holds, and no entry goes from settled back to unsettled. Under that
// lock the answer can only change from true to false (an entry settling),
// which merely refuses a purge that might have gone ahead. The deletes that
// follow touch only settled entries and done intents, rows the engine never
// locks.
const pendingTemplateLanding = `SELECT EXISTS (
	SELECT 1 FROM landing_entries entry
	WHERE entry.settled_at IS NULL AND (
		entry.compose_run_id IN (SELECT id FROM pipeline_runs WHERE template_pipeline_id = $1)
		OR entry.land_run_id IN (SELECT id FROM pipeline_runs WHERE template_pipeline_id = $1)
		OR EXISTS (
			SELECT 1 FROM landing_intents intent
			WHERE intent.entry_id = entry.id AND (
				intent.compose_run_id IN (SELECT id FROM pipeline_runs WHERE template_pipeline_id = $1)
				OR intent.land_run_id IN (SELECT id FROM pipeline_runs WHERE template_pipeline_id = $1)))))`

// purgeTemplateRuns deletes every Run of a template with its durable history
// and reports how many Runs went. The caller holds the team FOR SHARE and the
// template FOR UPDATE; the Runs are locked here, in id order, as every Run
// path locks them after their template.
func purgeTemplateRuns(ctx context.Context, tx Tx, templateID, teamID int) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM pipeline_runs WHERE template_pipeline_id = $1 ORDER BY id FOR UPDATE`, templateID)
	if err != nil {
		return 0, err
	}
	var runs []int
	for rows.Next() {
		var id int
		if err = rows.Scan(&id); err != nil {
			Close(rows)
			return 0, err
		}
		runs = append(runs, id)
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return 0, err
	}
	var active bool
	if err = tx.QueryRowContext(ctx, activeTemplateRun, templateID).Scan(&active); err != nil {
		return 0, err
	}
	if active {
		return 0, ErrPipelineTemplateHasActiveRun
	}
	var landing bool
	if err = tx.QueryRowContext(ctx, pendingTemplateLanding, templateID).Scan(&landing); err != nil {
		return 0, err
	}
	if landing {
		return 0, ErrPipelineTemplateHasPendingLanding
	}
	if _, err = tx.ExecContext(ctx, runEvidencePurgeMarker); err != nil {
		return 0, err
	}
	if err = purgeRunEvidence(ctx, tx, templateRunPurge, templateID); err != nil {
		return 0, err
	}
	// A Run's job builds log to the team's partition, payload or not; nothing
	// else ever deletes these once their builds are gone. Check builds log to
	// check_build_events, which check collection reaps once nothing points at
	// them.
	if _, err = tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM team_build_events_%d
		WHERE build_id IN (SELECT id FROM builds WHERE pipeline_run_id IN (`+templateRunPurge.runIDs+`))`, teamID), templateID); err != nil {
		return 0, err
	}
	for _, statement := range []string{
		`DELETE FROM builds WHERE pipeline_run_id IN (` + templateRunPurge.runIDs + `)`,
		`DELETE FROM task_caches WHERE template_pipeline_id = $1`,
		`DELETE FROM pipelines payload USING pipeline_runs run
		 WHERE payload.pipeline_run_id = run.id AND run.template_pipeline_id = $1`,
		`DELETE FROM pipeline_runs WHERE template_pipeline_id = $1`,
	} {
		if _, err = tx.ExecContext(ctx, statement, templateID); err != nil {
			return 0, err
		}
	}
	return len(runs), nil
}
