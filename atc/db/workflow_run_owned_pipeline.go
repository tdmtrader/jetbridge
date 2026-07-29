package db

import (
	"context"
	"errors"
)

var ErrWorkflowRunOwnedPipeline = errors.New("pipeline is owned by a durable workflow run")

// ErrWorkflowRunTemplateImmutable is returned by ordinary pipeline mutation
// surfaces for a server-owned durable workflow template.
var ErrWorkflowRunTemplateImmutable = errors.New("durable workflow-run template is immutable")

// rejectWorkflowResourceSourceMutation is the authority gate for public
// scheduler-selection mutations. It is deliberately source-only: resource
// comments, caches, and ordinary workflow-run template behavior retain their
// existing ownership rules.
func rejectWorkflowResourceSourceMutation(ctx context.Context, tx Tx, pipelineID int) error {
	if pipelineID <= 0 {
		return nil
	}
	var sourceOwned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_workflow_resource_source_pipelines WHERE pipeline_id = $1
		)
	`, pipelineID).Scan(&sourceOwned); err != nil {
		return err
	}
	if sourceOwned {
		return ErrAgentWorkflowResourceSourceImmutable
	}
	return nil
}

// rejectWorkflowRunConfigMutation protects server-owned base templates,
// standing source pipelines, and materialized workflow-run instances from
// ordinary config writes.
func rejectWorkflowRunConfigMutation(ctx context.Context, tx Tx, pipelineID int) error {
	if pipelineID <= 0 {
		return nil
	}
	var templateOwned, instanceOwned, sourceOwned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM agent_workflow_run_templates WHERE pipeline_id = $1),
		  EXISTS (SELECT 1 FROM agent_workflow_runs WHERE instance_pipeline_id = $1),
		  EXISTS (SELECT 1 FROM agent_workflow_resource_source_pipelines WHERE pipeline_id = $1)
	`, pipelineID).Scan(&templateOwned, &instanceOwned, &sourceOwned); err != nil {
		return err
	}
	if sourceOwned {
		return ErrAgentWorkflowResourceSourceImmutable
	}
	if templateOwned {
		return ErrWorkflowRunTemplateImmutable
	}
	if instanceOwned {
		return ErrWorkflowRunOwnedPipeline
	}
	return nil
}

// rejectWorkflowRunTemplateMutation protects server-owned base templates and
// standing source-selection pipelines. Instance pipelines produced from a
// template are deliberately excluded from this narrower lifecycle guard.
func rejectWorkflowRunTemplateMutation(ctx context.Context, tx Tx, pipelineID int) error {
	if pipelineID <= 0 {
		return nil
	}
	var templateOwned, sourceOwned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  EXISTS (SELECT 1 FROM agent_workflow_run_templates WHERE pipeline_id = $1),
		  EXISTS (SELECT 1 FROM agent_workflow_resource_source_pipelines WHERE pipeline_id = $1)
	`, pipelineID).Scan(&templateOwned, &sourceOwned); err != nil {
		return err
	}
	if sourceOwned {
		return ErrAgentWorkflowResourceSourceImmutable
	}
	if templateOwned {
		return ErrWorkflowRunTemplateImmutable
	}
	return nil
}

// rejectWorkflowRunOwnedPipeline is called inside the same transaction as a
// public manual execution write. Durable workflow and standing source
// ownership are authoritative; caller-supplied pipeline shape is irrelevant.
func rejectWorkflowRunOwnedPipeline(ctx context.Context, tx Tx, pipelineID int) error {
	if pipelineID <= 0 {
		return nil
	}
	var owned, sourceOwned bool
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  EXISTS (
			SELECT 1 FROM agent_workflow_runs WHERE instance_pipeline_id = $1
			UNION ALL
			SELECT 1 FROM agent_workflow_run_templates WHERE pipeline_id = $1
		  ),
		  EXISTS (SELECT 1 FROM agent_workflow_resource_source_pipelines WHERE pipeline_id = $1)
	`, pipelineID).Scan(&owned, &sourceOwned); err != nil {
		return err
	}
	if sourceOwned {
		return ErrAgentWorkflowResourceSourceImmutable
	}
	if owned {
		return ErrWorkflowRunOwnedPipeline
	}
	return nil
}
