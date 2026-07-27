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

// WorkflowRunTemplateLifecycle reclaims the server-owned one-shot templates
// that workflow admission mints but never executes. A template is minted at
// admission for every distinct (workflow, version, rendered-config) identity,
// and an admission that fails after TemplateSaver.SaveOrReuse but before the
// execution row is created leaves the pipeline row behind forever.
//
// Only never-executed templates are reclaimable. pipeline_runs.template_pipeline_id
// is NOT NULL ON DELETE CASCADE, so destroying a template that ever ran would
// silently delete its execution history along with it; reclaiming those is a
// separate retention policy that must destroy the run instance pipeline, the
// pipeline_runs row, and the template as one unit. By contrast the durable
// record in agent_workflow_runs carries no foreign key to any of them (dropped
// deliberately in migration 1773106103) — its execution provenance is immutable
// scalars, so a durable run ID stays citable whether or not the plumbing it
// names still exists.
//
//counterfeiter:generate . WorkflowRunTemplateLifecycle
type WorkflowRunTemplateLifecycle interface {
	// RemoveAbandonedWorkflowRunTemplates destroys up to limit owned templates
	// that have no execution history and were registered more than gracePeriod
	// ago, and reports how many were destroyed.
	RemoveAbandonedWorkflowRunTemplates(ctx context.Context, gracePeriod time.Duration, limit int) (int, error)
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
