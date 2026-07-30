ALTER TABLE agent_workflow_definitions
    ADD COLUMN definition_kind TEXT NOT NULL DEFAULT 'workflow',
    ADD COLUMN released_at TIMESTAMPTZ,
    ADD COLUMN released_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN deprecated_at TIMESTAMPTZ,
    ADD COLUMN deprecated_by TEXT NOT NULL DEFAULT '',
    ADD COLUMN release_predecessor_version INTEGER,
    ADD COLUMN release_compatibility TEXT;

ALTER TABLE agent_workflow_definitions
    DROP CONSTRAINT agent_workflow_definitions_name_version_key;
DROP INDEX agent_workflow_definitions_hash;
DROP INDEX agent_workflow_definitions_live;

ALTER TABLE agent_workflow_definitions
    ADD CONSTRAINT agent_workflow_definitions_kind_check
        CHECK (definition_kind IN ('workflow', 'node')),
    ADD CONSTRAINT agent_workflow_definitions_kind_version_key
        UNIQUE (definition_kind, name, version),
    ADD CONSTRAINT agent_workflow_definitions_kind_hash_key
        UNIQUE (definition_kind, name, content_hash),
    ADD CONSTRAINT agent_workflow_definitions_node_runtime_metadata_check
        CHECK (definition_kind <> 'node' OR (schema_version = 3 AND signature_version > 0)),
    ADD CONSTRAINT agent_workflow_definitions_live_kind_check
        CHECK (NOT live OR definition_kind = 'workflow'),
    ADD CONSTRAINT agent_workflow_definitions_release_metadata_check
        CHECK (
            (definition_kind = 'workflow'
             AND released_at IS NULL
             AND released_by = ''
             AND deprecated_at IS NULL
             AND deprecated_by = ''
             AND release_predecessor_version IS NULL
             AND release_compatibility IS NULL)
            OR (definition_kind = 'node' AND (
                (released_at IS NULL
                 AND released_by = ''
                 AND release_predecessor_version IS NULL
                 AND release_compatibility IS NULL)
                OR (released_at IS NOT NULL
                    AND btrim(released_by) <> ''
                    AND release_compatibility IN ('compatible', 'breaking')
                    AND (release_predecessor_version IS NULL OR release_predecessor_version > 0))
            ) AND (
                (deprecated_at IS NULL AND deprecated_by = '')
                OR (deprecated_at IS NOT NULL AND btrim(deprecated_by) <> '')
            ))
        );

CREATE UNIQUE INDEX agent_workflow_definitions_live
    ON agent_workflow_definitions (definition_kind, name)
    WHERE live;
CREATE INDEX agent_workflow_definitions_released_nodes
    ON agent_workflow_definitions (name, version DESC)
    WHERE definition_kind = 'node' AND released_at IS NOT NULL;

ALTER TABLE agent_workflow_runs
    ADD COLUMN definition_kind TEXT NOT NULL DEFAULT 'workflow';

ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT agent_workflow_runs_team_id_idempotency_key_key,
    DROP CONSTRAINT agent_workflow_runs_retry_of_workflow_run_id_fkey;

ALTER TABLE agent_workflow_runs
    ADD CONSTRAINT agent_workflow_runs_definition_kind_check
        CHECK (definition_kind IN ('workflow', 'node')),
    ADD CONSTRAINT agent_workflow_runs_team_kind_idempotency_key
        UNIQUE (team_id, definition_kind, idempotency_key),
    ADD CONSTRAINT agent_workflow_runs_id_definition_kind_key
        UNIQUE (id, definition_kind),
    ADD CONSTRAINT agent_workflow_runs_retry_kind_fkey
        FOREIGN KEY (retry_of_workflow_run_id, definition_kind)
        REFERENCES agent_workflow_runs (id, definition_kind);

CREATE INDEX agent_workflow_runs_team_kind_workflow_created
    ON agent_workflow_runs (team_id, definition_kind, workflow_name, created_at DESC, id DESC);
