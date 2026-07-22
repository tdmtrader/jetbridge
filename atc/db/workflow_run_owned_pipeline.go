package db

import (
	"context"
	"errors"
)

var ErrWorkflowRunOwnedPipeline = errors.New("pipeline is owned by a durable workflow run")

// ErrWorkflowRunTemplateImmutable is returned by ordinary pipeline mutation
// surfaces for a server-owned durable workflow template.
var ErrWorkflowRunTemplateImmutable = errors.New("durable workflow-run template is immutable")

// rejectWorkflowRunTemplateMutation protects only the server-owned base
// template. Instance pipelines produced from the template are deliberately
// excluded: durable workflow execution still owns their normal run lifecycle.
func rejectWorkflowRunTemplateMutation(ctx context.Context, tx Tx, pipelineID int) error {
	if pipelineID <= 0 {
		return nil
	}
	var owned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_run_templates
			WHERE pipeline_id = $1
		)
	`, pipelineID).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return ErrWorkflowRunTemplateImmutable
	}
	return nil
}

// rejectWorkflowRunOwnedPipeline is called inside the same transaction as a
// public manual execution write. Durable workflow ownership is authoritative;
// a caller-supplied instance var, template-shaped config, or plan annotation is
// never considered. The explicit template registry closes the interval before
// the first agent_workflow_runs execution link exists.
func rejectWorkflowRunOwnedPipeline(ctx context.Context, tx Tx, pipelineID int) error {
	if pipelineID <= 0 {
		return nil
	}
	var owned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs
			WHERE instance_pipeline_id = $1
			UNION ALL
			SELECT 1
			FROM agent_workflow_run_templates
			WHERE pipeline_id = $1
		)
	`, pipelineID).Scan(&owned); err != nil {
		return err
	}
	if owned {
		return ErrWorkflowRunOwnedPipeline
	}
	return nil
}
