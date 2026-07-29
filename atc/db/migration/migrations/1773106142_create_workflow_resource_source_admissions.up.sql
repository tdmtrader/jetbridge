CREATE TABLE agent_workflow_resource_source_pipelines (
    pipeline_id INTEGER PRIMARY KEY REFERENCES pipelines (id) ON DELETE RESTRICT,
    team_id INTEGER NOT NULL REFERENCES teams (id) ON DELETE RESTRICT,
    workflow_definition_id INTEGER NOT NULL REFERENCES agent_workflow_definitions (id) ON DELETE RESTRICT,
    workflow_name TEXT NOT NULL CHECK (btrim(workflow_name) <> ''),
    workflow_version INTEGER NOT NULL CHECK (workflow_version > 0),
    pipeline_config_version INTEGER NOT NULL CHECK (pipeline_config_version > 0),
    config_hash TEXT NOT NULL CHECK (config_hash ~ '^[0-9a-f]{64}$'),
    source_declarations JSONB NOT NULL CHECK (jsonb_typeof(source_declarations) = 'array'),
    state TEXT NOT NULL CHECK (state IN ('active', 'draining', 'archived')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, workflow_definition_id),
    UNIQUE (team_id, pipeline_id, workflow_definition_id)
);
CREATE UNIQUE INDEX agent_workflow_resource_source_pipelines_active
    ON agent_workflow_resource_source_pipelines (team_id, workflow_name) WHERE state = 'active';

CREATE TABLE agent_workflow_resource_source_admissions (
    id BIGSERIAL PRIMARY KEY,
    team_id INTEGER NOT NULL REFERENCES teams (id) ON DELETE RESTRICT,
    workflow_definition_id INTEGER NOT NULL REFERENCES agent_workflow_definitions (id) ON DELETE RESTRICT,
    source_pipeline_id INTEGER NOT NULL,
    source_config_hash TEXT NOT NULL CHECK (source_config_hash ~ '^[0-9a-f]{64}$'),
    idempotency_key TEXT NOT NULL CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 512),
    mode TEXT NOT NULL CHECK (mode IN ('manual', 'automatic')),
    selecting_build_id BIGINT REFERENCES builds (id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('selecting', 'capturing', 'ready', 'failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (team_id, idempotency_key),
    UNIQUE (id, source_pipeline_id),
    UNIQUE (id, team_id, workflow_definition_id),
    UNIQUE (id, team_id, workflow_definition_id, source_config_hash),
    CHECK (mode = 'manual' OR selecting_build_id IS NOT NULL),
    CHECK (status IN ('selecting', 'failed') OR selecting_build_id IS NOT NULL),
    FOREIGN KEY (team_id, source_pipeline_id, workflow_definition_id)
      REFERENCES agent_workflow_resource_source_pipelines (team_id, pipeline_id, workflow_definition_id) ON DELETE RESTRICT
);
CREATE UNIQUE INDEX agent_workflow_resource_source_admissions_build
    ON agent_workflow_resource_source_admissions (source_pipeline_id, selecting_build_id) WHERE selecting_build_id IS NOT NULL;

CREATE TABLE agent_workflow_resource_source_bindings (
    admission_id BIGINT NOT NULL REFERENCES agent_workflow_resource_source_admissions (id) ON DELETE CASCADE,
    source_name TEXT NOT NULL CHECK (btrim(source_name) <> ''),
    resource_name TEXT NOT NULL CHECK (btrim(resource_name) <> ''),
    selecting_build_id BIGINT NOT NULL REFERENCES builds (id) ON DELETE RESTRICT,
    source_pipeline_id INTEGER NOT NULL REFERENCES pipelines (id) ON DELETE RESTRICT,
    pipeline_config_version INTEGER NOT NULL CHECK (pipeline_config_version > 0),
    resource_id INTEGER NOT NULL REFERENCES resources (id) ON DELETE RESTRICT,
    resource_config_version_id BIGINT NOT NULL REFERENCES resource_config_versions (id) ON DELETE RESTRICT,
    resource_version_id BIGINT NOT NULL REFERENCES resource_config_versions (id) ON DELETE RESTRICT,
    version_digest TEXT NOT NULL CHECK (version_digest ~ '^[0-9a-f]{32}([0-9a-f]{32})?$'),
    version JSONB NOT NULL CHECK (jsonb_typeof(version) = 'object'),
    snapshot_type_name TEXT NOT NULL CHECK (snapshot_type_name ~ '^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$'),
    snapshot_type_version INTEGER NOT NULL CHECK (snapshot_type_version > 0),
    capture_operation_key TEXT NOT NULL CHECK (btrim(capture_operation_key) <> '' AND octet_length(capture_operation_key) <= 512),
    snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    captured_at TIMESTAMPTZ,
    PRIMARY KEY (admission_id, source_name),
    UNIQUE (admission_id, capture_operation_key),
    FOREIGN KEY (admission_id, source_pipeline_id)
      REFERENCES agent_workflow_resource_source_admissions (id, source_pipeline_id) ON DELETE CASCADE,
    CHECK (resource_config_version_id = resource_version_id),
    CHECK ((snapshot_id IS NULL) = (captured_at IS NULL))
);

ALTER TABLE agent_workflow_runs ADD COLUMN resource_source_admission_id BIGINT;
ALTER TABLE agent_workflow_runs ADD CONSTRAINT agent_workflow_runs_resource_source_admission_fkey
    FOREIGN KEY (resource_source_admission_id, team_id, workflow_definition_id)
    REFERENCES agent_workflow_resource_source_admissions (id, team_id, workflow_definition_id) ON DELETE RESTRICT;
CREATE INDEX agent_workflow_runs_resource_source_admission
    ON agent_workflow_runs (resource_source_admission_id, id) WHERE resource_source_admission_id IS NOT NULL;
