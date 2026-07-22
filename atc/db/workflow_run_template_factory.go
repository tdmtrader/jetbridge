package db

import (
	"context"
	"database/sql"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
)

// WorkflowRunTemplateFactory owns the authoritative registry for immutable
// templates that may only be executed through durable workflow admission.
// Ordinary Concourse templates never enter this registry.
type WorkflowRunTemplateFactory interface {
	SaveWorkflowRunTemplate(context.Context, int, atc.PipelineRef, atc.Config) (Pipeline, bool, error)
	IsWorkflowRunTemplate(context.Context, int) (bool, error)
	DestroyUnusedWorkflowRunTemplate(context.Context, int) (bool, error)
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

// DestroyUnusedWorkflowRunTemplate is the explicit server-owned cleanup path.
// It can remove a template abandoned before execution admission, but it will
// never erase a template once pipeline-run or durable workflow history points
// at it. Ordinary Pipeline.Destroy remains guarded for every marked template.
func (f *workflowRunTemplateFactory) DestroyUnusedWorkflowRunTemplate(ctx context.Context, pipelineID int) (bool, error) {
	if pipelineID <= 0 {
		return false, nil
	}
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)

	var destroyedID int
	err = tx.QueryRowContext(ctx, `
		DELETE FROM pipelines AS pipeline
		WHERE pipeline.id = $1
		  AND EXISTS (
			SELECT 1
			FROM agent_workflow_run_templates owned
			WHERE owned.pipeline_id = pipeline.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM pipeline_runs execution
			WHERE execution.template_pipeline_id = pipeline.id
		  )
		  AND NOT EXISTS (
			SELECT 1
			FROM agent_workflow_runs durable
			WHERE durable.template_pipeline_id = pipeline.id
		  )
		RETURNING pipeline.id
	`, pipelineID).Scan(&destroyedID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	f.conn.Bus().Notify(atc.ComponentCollectorPipelines)
	return true, nil
}
