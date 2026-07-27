package db

import (
	"context"
	"database/sql"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
)

// WorkflowRunTemplateFactory owns the authoritative registry for immutable
// templates that may only be executed through durable workflow admission.
// Ordinary Concourse templates never enter this registry.
//
// Destruction is deliberately not part of this interface: reclaiming an
// abandoned template is WorkflowRunTemplateLifecycle's job, which holds the
// row lock and honours the admission grace period. A second entry point would
// be a second copy of that predicate to keep in sync.
type WorkflowRunTemplateFactory interface {
	SaveWorkflowRunTemplate(context.Context, int, atc.PipelineRef, atc.Config) (Pipeline, bool, error)
	IsWorkflowRunTemplate(context.Context, int) (bool, error)
}

func NewWorkflowRunTemplateFactory(conn DbConn, lockFactory lock.LockFactory) WorkflowRunTemplateFactory {
	return &workflowRunTemplateFactory{conn: conn, lockFactory: lockFactory}
}

type workflowRunTemplateFactory struct {
	conn        DbConn
	lockFactory lock.LockFactory
}

// SaveWorkflowRunTemplate creates the pipeline and records server ownership in
// one transaction. It is deliberately create-only: an exact ordinary pipeline
// that won the name first is a collision and must never be claimed after the
// fact.
func (f *workflowRunTemplateFactory) SaveWorkflowRunTemplate(
	ctx context.Context,
	teamID int,
	pipelineRef atc.PipelineRef,
	config atc.Config,
) (Pipeline, bool, error) {
	if teamID <= 0 || pipelineRef.Name == "" || pipelineRef.InstanceVars != nil || !config.Template {
		return nil, false, ErrNotATemplate
	}
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer Rollback(tx)

	nullID := sql.NullInt64{Valid: false}
	pipelineID, created, err := savePipeline(tx, pipelineRef, config, ConfigVersion(0), false, teamID, nullID, nullID)
	if err != nil {
		return nil, false, err
	}
	if !created {
		return nil, false, ErrConfigComparisonFailed
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_run_templates (pipeline_id)
		VALUES ($1)
	`, pipelineID); err != nil {
		return nil, false, err
	}

	pipeline := newPipeline(f.conn, f.lockFactory)
	if err := scanPipeline(
		pipeline,
		pipelinesQuery.Where(sq.Eq{"p.id": pipelineID}).RunWith(tx).QueryRow(),
	); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return pipeline, true, nil
}

func (f *workflowRunTemplateFactory) IsWorkflowRunTemplate(ctx context.Context, pipelineID int) (bool, error) {
	if pipelineID <= 0 {
		return false, nil
	}
	var owned bool
	err := f.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_run_templates
			WHERE pipeline_id = $1
		)
	`, pipelineID).Scan(&owned)
	return owned, err
}
