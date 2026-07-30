DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_workflow_definitions WHERE definition_kind = 'node')
       OR EXISTS (SELECT 1 FROM agent_workflow_runs WHERE definition_kind = 'node') THEN
        RAISE EXCEPTION 'cannot downgrade reusable node definitions while node rows exist';
    END IF;
END $$;

DROP INDEX agent_workflow_runs_team_kind_workflow_created;

DROP TRIGGER agent_workflow_runs_retry_definition_kind ON agent_workflow_runs;
DROP FUNCTION agent_workflow_runs_enforce_retry_definition_kind();

ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT agent_workflow_runs_team_kind_idempotency_key,
    DROP CONSTRAINT agent_workflow_runs_definition_kind_check,
    DROP COLUMN definition_kind;

ALTER TABLE agent_workflow_runs
    ADD CONSTRAINT agent_workflow_runs_team_id_idempotency_key_key
        UNIQUE (team_id, idempotency_key);

DROP INDEX agent_workflow_definitions_released_nodes;
DROP INDEX agent_workflow_definitions_live;

ALTER TABLE agent_workflow_definitions
    DROP CONSTRAINT agent_workflow_definitions_release_metadata_check,
    DROP CONSTRAINT agent_workflow_definitions_live_kind_check,
    DROP CONSTRAINT agent_workflow_definitions_node_runtime_metadata_check,
    DROP CONSTRAINT agent_workflow_definitions_kind_hash_key,
    DROP CONSTRAINT agent_workflow_definitions_kind_version_key,
    DROP CONSTRAINT agent_workflow_definitions_kind_check,
    DROP COLUMN release_compatibility,
    DROP COLUMN release_predecessor_version,
    DROP COLUMN deprecated_by,
    DROP COLUMN deprecated_at,
    DROP COLUMN released_by,
    DROP COLUMN released_at,
    DROP COLUMN definition_kind;

ALTER TABLE agent_workflow_definitions
    ADD CONSTRAINT agent_workflow_definitions_name_version_key UNIQUE (name, version);
CREATE UNIQUE INDEX agent_workflow_definitions_hash
    ON agent_workflow_definitions (name, content_hash);
CREATE UNIQUE INDEX agent_workflow_definitions_live
    ON agent_workflow_definitions (name) WHERE live;
