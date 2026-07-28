package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
)

// MaxAbandonedWorkflowRunTemplateBatch bounds one collection pass so a large
// backlog is drained over several passes instead of one long transaction that
// holds row locks against admission.
const MaxAbandonedWorkflowRunTemplateBatch = 500

// MaxRetiredWorkflowRunTemplateBatch bounds one retirement pass for the same
// reason: every candidate's row lock is held for the whole transaction.
const MaxRetiredWorkflowRunTemplateBatch = 500

// WorkflowRunTemplateLifecycle reclaims the server-owned one-shot templates
// that workflow admission mints, in two tiers. A template is minted at
// admission for every distinct (workflow, version, rendered-config) identity.
//
// Tier 1 (RemoveAbandonedWorkflowRunTemplates) reclaims templates that never
// executed: an admission that fails after TemplateSaver.SaveOrReuse but
// before the execution row is created leaves the pipeline row behind forever.
//
// Tier 2 (RemoveRetiredWorkflowRunTemplates) reclaims executed templates.
// pipeline_runs.template_pipeline_id is NOT NULL ON DELETE CASCADE, so
// destroying a template that ever ran deletes its execution history along
// with it; the retired pass therefore only fires once that history is dead
// weight — workflow version superseded by a newer live definition, every
// citing durable run terminal, every run archived and past retention — and
// destroys the run instance pipelines, the pipeline_runs rows, and the
// template as one unit.
//
// The reclaimed unit is wider than those three tables: builds_pipeline_id_fkey
// is ON DELETE CASCADE, so each instance pipeline takes its builds with it
// (and, through the deleted_pipelines trigger, its pipeline_build_events
// table). Rows that reference those builds by plain integer — agent_run_metrics,
// agent_cost_ledger, agent_run_transcripts, agent_snapshot_productions — carry
// no foreign key and survive; readers join builds LEFT, so a reclaimed build
// reads as absent rather than as an error. Anything that needs a *fact* about
// a reclaimed build must read it from the durable record instead: that is why
// AgentRunMetricsFactory.WorkflowStats counts success from
// agent_workflow_runs.execution_status once the build is gone.
//
// By contrast the durable record in agent_workflow_runs carries no foreign key
// to any of them (dropped deliberately in migration 1773106103) — its execution
// provenance is immutable scalars, so a durable run ID stays citable whether or
// not the plumbing it names still exists.
//
//counterfeiter:generate . WorkflowRunTemplateLifecycle
type WorkflowRunTemplateLifecycle interface {
	// RemoveAbandonedWorkflowRunTemplates destroys up to limit owned templates
	// that have no execution history and were registered more than gracePeriod
	// ago, and reports how many were destroyed.
	RemoveAbandonedWorkflowRunTemplates(ctx context.Context, gracePeriod time.Duration, limit int) (int, error)

	// RemoveRetiredWorkflowRunTemplates destroys up to limit owned templates
	// whose execution history is fully archived and older than
	// retirementPeriod, whose citing durable runs are all terminal, and whose
	// workflow version has a strictly newer live successor. Each template is
	// destroyed together with its run instance pipelines and pipeline_runs
	// rows as one unit. It reports how many templates were destroyed.
	RemoveRetiredWorkflowRunTemplates(ctx context.Context, retirementPeriod time.Duration, limit int) (int, error)
}

func NewWorkflowRunTemplateLifecycle(conn DbConn) WorkflowRunTemplateLifecycle {
	return &workflowRunTemplateLifecycle{conn: conn}
}

type workflowRunTemplateLifecycle struct {
	conn DbConn
}

func (l *workflowRunTemplateLifecycle) RemoveAbandonedWorkflowRunTemplates(
	ctx context.Context,
	gracePeriod time.Duration,
	limit int,
) (int, error) {
	if gracePeriod <= 0 {
		return 0, fmt.Errorf("db: workflow-run template grace period must be positive")
	}
	if limit <= 0 || limit > MaxAbandonedWorkflowRunTemplateBatch {
		return 0, fmt.Errorf("db: workflow-run template batch must be between 1 and %d", MaxAbandonedWorkflowRunTemplateBatch)
	}

	tx, err := l.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	// Claim the same pipelines row lock that validateLockedWorkflowTemplate and
	// validateLockedServerTemplate take for the whole of an execution-creating
	// transaction: a template being admitted right now is already locked and is
	// skipped, and one claimed here cannot start being admitted until this
	// transaction ends. SKIP LOCKED keeps a busy template from stalling the pass.
	rows, err := tx.QueryContext(ctx, `
		SELECT template.id
		FROM agent_workflow_run_templates owned
		JOIN pipelines template ON template.id = owned.pipeline_id
		WHERE owned.created_at < now() - $1::interval
		  AND `+abandonedWorkflowRunTemplateGuards+`
		ORDER BY template.id
		LIMIT $2
		FOR UPDATE OF template SKIP LOCKED
	`, gracePeriod.String(), limit)
	if err != nil {
		return 0, err
	}
	var candidates []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			Close(rows)
			return 0, err
		}
		candidates = append(candidates, id)
	}
	if err := rows.Err(); err != nil {
		Close(rows)
		return 0, err
	}
	Close(rows)
	if len(candidates) == 0 {
		return 0, nil
	}

	destroyed := 0
	for _, id := range candidates {
		// Re-checked as its own statement so it reads a snapshot taken after
		// the lock was claimed rather than the one the candidate scan used.
		var destroyedID int
		err := tx.QueryRowContext(ctx, `
			DELETE FROM pipelines AS template
			WHERE template.id = $1
			  AND EXISTS (
				SELECT 1
				FROM agent_workflow_run_templates owned
				WHERE owned.pipeline_id = template.id
				  AND owned.created_at < now() - $2::interval
			  )
			  AND `+abandonedWorkflowRunTemplateGuards+`
			RETURNING template.id
		`, id, gracePeriod.String()).Scan(&destroyedID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		destroyed++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if destroyed > 0 {
		l.conn.Bus().Notify(atc.ComponentCollectorPipelines)
	}
	return destroyed, nil
}

func (l *workflowRunTemplateLifecycle) RemoveRetiredWorkflowRunTemplates(
	ctx context.Context,
	retirementPeriod time.Duration,
	limit int,
) (int, error) {
	if retirementPeriod <= 0 {
		return 0, fmt.Errorf("db: workflow-run template retirement period must be positive")
	}
	if limit <= 0 || limit > MaxRetiredWorkflowRunTemplateBatch {
		return 0, fmt.Errorf("db: workflow-run template batch must be between 1 and %d", MaxRetiredWorkflowRunTemplateBatch)
	}

	tx, err := l.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	// Same locking discipline as the abandoned pass: the candidate scan claims
	// the pipelines row lock that every execution-creating transaction takes on
	// its template, so a template being admitted right now is skipped and one
	// claimed here cannot start being admitted until this transaction ends.
	rows, err := tx.QueryContext(ctx, `
		SELECT template.id
		FROM agent_workflow_run_templates owned
		JOIN pipelines template ON template.id = owned.pipeline_id
		WHERE `+retiredWorkflowRunTemplateGuards+`
		ORDER BY template.id
		LIMIT $2
		FOR UPDATE OF template SKIP LOCKED
	`, retirementPeriod.String(), limit)
	if err != nil {
		return 0, err
	}
	var candidates []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			Close(rows)
			return 0, err
		}
		candidates = append(candidates, id)
	}
	if err := rows.Err(); err != nil {
		Close(rows)
		return 0, err
	}
	Close(rows)
	if len(candidates) == 0 {
		return 0, nil
	}

	destroyed := 0
	for _, id := range candidates {
		// One statement destroys the whole unit — run instance pipelines,
		// pipeline_runs (cascading from the template), and the template — so
		// no partial reclaim can ever commit. The guards are re-checked inside
		// the same statement so they read a snapshot taken after the lock was
		// claimed rather than the one the candidate scan used.
		var destroyedID int
		err := tx.QueryRowContext(ctx, `
			WITH qualified AS (
				SELECT template.id, template.team_id, template.name
				FROM pipelines template
				JOIN agent_workflow_run_templates owned ON owned.pipeline_id = template.id
				WHERE template.id = $2
				  AND `+retiredWorkflowRunTemplateGuards+`
			),
			gone_instances AS (
				DELETE FROM pipelines AS instance
				USING qualified
				WHERE instance.team_id = qualified.team_id
				  AND instance.name = qualified.name
				  AND instance.id <> qualified.id
				RETURNING instance.id
			)
			DELETE FROM pipelines AS template
			USING qualified
			WHERE template.id = qualified.id
			RETURNING template.id
		`, retirementPeriod.String(), id).Scan(&destroyedID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		destroyed++
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if destroyed > 0 {
		l.conn.Bus().Notify(atc.ComponentCollectorPipelines)
	}
	return destroyed, nil
}

// retiredWorkflowRunTemplateGuards is the tier-2 destruction predicate, shared
// by the candidate scan and the locked re-check so the two can never disagree.
// It is evaluated with `template` bound to the candidate pipelines row and $1
// bound to the retirement interval, and admits only templates whose entire
// execution history is dead weight:
//
//   - executed at least once, and every pipeline_run is archived, finished,
//     and completed more than the retirement period ago;
//   - cited by at least one durable workflow run — an executed owned template
//     with no citation is a resource-capture template, whose output reads
//     still join through it until finalization — and every citation is
//     terminal, because pre-terminal readers (execution linking, build
//     association, workflow-wait authorization) join through
//     template_pipeline_id;
//   - superseded: every cited workflow version has a strictly newer live
//     definition of the same name. A draft successor or a rolled-back live
//     version does not retire the history it may still need;
//   - past the template's own run_retention, evaluated exactly as
//     PipelineRunFactory.RunsToArchive does — keep_last therefore keeps the
//     template alive for as long as it keeps any run, since some run always
//     holds rank 1;
//   - free of anything else alive under its name: every same-named instance
//     pipeline is archived and linked from this template's runs (a stray
//     instance a user parked under the name is never deleted), and no linked
//     instance still has an unfinished build;
//   - not on a team with an admission that has not linked its template yet,
//     for the same reason as the abandoned pass.
const retiredWorkflowRunTemplateGuards = `
		  EXISTS (
			SELECT 1
			FROM pipeline_runs execution
			WHERE execution.template_pipeline_id = template.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM pipeline_runs execution
			WHERE execution.template_pipeline_id = template.id
			  AND (execution.archived = false
			       OR execution.status = 'running'
			       OR execution.completed_at IS NULL
			       OR execution.completed_at >= now() - $1::interval)
		  )
		  AND EXISTS (
			SELECT 1
			FROM agent_workflow_runs durable
			WHERE durable.template_pipeline_id = template.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM agent_workflow_runs durable
			WHERE durable.template_pipeline_id = template.id
			  AND (durable.status NOT IN ('succeeded', 'failed', 'errored', 'aborted')
			       OR NOT EXISTS (
				SELECT 1
				FROM agent_workflow_definitions successor
				WHERE successor.name = durable.workflow_name
				  AND successor.live
				  AND successor.version > durable.workflow_version
			  ))
		  )
		  AND NOT (template.run_retention IS NOT NULL AND EXISTS (
			SELECT 1
			FROM pipeline_runs kept
			WHERE kept.template_pipeline_id = template.id
			  AND NOT (
				(template.run_retention ? 'keep_last'
				 AND (SELECT COUNT(*)
				      FROM pipeline_runs newer
				      WHERE newer.template_pipeline_id = template.id
				        AND newer.number >= kept.number) > (template.run_retention->>'keep_last')::int)
				OR (template.run_retention ? 'ttl_days'
				    AND kept.completed_at IS NOT NULL
				    AND kept.completed_at < now() - make_interval(days => (template.run_retention->>'ttl_days')::int))
			  )
		  ))
		  AND NOT EXISTS (
			SELECT 1
			FROM pipelines instance
			WHERE instance.team_id = template.team_id
			  AND instance.name = template.name
			  AND instance.id <> template.id
			  AND (instance.archived = false
			       OR NOT EXISTS (
				SELECT 1
				FROM pipeline_runs linked
				WHERE linked.template_pipeline_id = template.id
				  AND linked.instance_pipeline_id = instance.id
			  ))
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM pipeline_runs execution
			JOIN builds unfinished ON unfinished.pipeline_id = execution.instance_pipeline_id
			WHERE execution.template_pipeline_id = template.id
			  AND unfinished.status IN ('pending', 'started')
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM agent_workflow_runs admitting
			WHERE admitting.team_id = template.team_id
			  AND admitting.status = 'admitting'
			  AND admitting.template_pipeline_id IS NULL
		  )`

// abandonedWorkflowRunTemplateGuards is the destruction predicate, shared by
// the candidate scan and the locked re-check so the two can never disagree.
// It is evaluated with `template` bound to the candidate pipelines row, and
// requires that the template has no pipeline_runs (which would cascade-delete
// with it), that no durable workflow run cites it (there is no foreign key to
// notice if one did), and that no run instance was materialized under its
// name (instances survive their template and would be orphaned).
//
// The last guard defers the whole team while any admission holds a template
// reference it has not linked yet: between TemplateSaver.SaveOrReuse and the
// execution-creating transaction there is nothing pointing at the template and
// no row lock held, so an admission reusing an older abandoned template would
// otherwise be able to lose it. Unlinked admissions are bounded by
// --agent-workflow-run-admission-timeout.
const abandonedWorkflowRunTemplateGuards = `
		  NOT EXISTS (
			SELECT 1
			FROM pipeline_runs execution
			WHERE execution.template_pipeline_id = template.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM agent_workflow_runs durable
			WHERE durable.template_pipeline_id = template.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM pipelines instance
			WHERE instance.team_id = template.team_id
			  AND instance.name = template.name
			  AND instance.id <> template.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM agent_workflow_runs admitting
			WHERE admitting.team_id = template.team_id
			  AND admitting.status = 'admitting'
			  AND admitting.template_pipeline_id IS NULL
		  )`
