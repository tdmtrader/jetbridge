DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_workflow_resource_source_pipelines
        WHERE pr_binding_id IS NOT NULL
    ) THEN
        RAISE EXCEPTION
            'cannot remove agent PR bindings while binding-owned source pipelines exist';
    END IF;
END
$$;

DROP INDEX agent_workflow_resource_source_pipelines_active;
DROP INDEX agent_workflow_resource_source_pipelines_binding;
DROP INDEX agent_workflow_resource_source_pipelines_definition;

ALTER TABLE agent_workflow_resource_source_pipelines
    DROP COLUMN pr_binding_id,
    ADD UNIQUE (team_id, workflow_definition_id);

CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_active
    ON agent_workflow_resource_source_pipelines (team_id, workflow_name)
    WHERE state = 'active';

DROP TABLE agent_pr_bindings;
