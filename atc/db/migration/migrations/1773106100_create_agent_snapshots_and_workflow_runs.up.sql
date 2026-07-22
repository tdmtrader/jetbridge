CREATE TABLE agent_snapshots (
    id                 BIGSERIAL PRIMARY KEY,
    type_name          TEXT NOT NULL CHECK (type_name ~ '^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$'),
    type_version       INTEGER NOT NULL CHECK (type_version > 0),
    digest             TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    byte_size          BIGINT NOT NULL CHECK (byte_size >= 0),
    file_count         BIGINT NOT NULL CHECK (file_count >= 0),
    representation     TEXT NOT NULL CHECK (btrim(representation) <> ''),
    intrinsic_metadata JSONB,
    content_state      TEXT NOT NULL DEFAULT 'available'
                       CHECK (content_state IN ('available', 'expired')),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (type_name, type_version, digest)
);

CREATE INDEX agent_snapshots_digest ON agent_snapshots (digest);
CREATE INDEX agent_snapshots_type_state_created
    ON agent_snapshots (type_name, type_version, content_state, created_at, id);

CREATE TABLE agent_snapshot_locations (
    id         BIGSERIAL PRIMARY KEY,
    digest     TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    driver     TEXT NOT NULL CHECK (btrim(driver) <> ''),
    key        TEXT NOT NULL CHECK (btrim(key) <> ''),
    node       TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (digest, driver, key, node)
);

CREATE INDEX agent_snapshot_locations_digest ON agent_snapshot_locations (digest);

CREATE TABLE agent_snapshot_staged_uploads (
    id               BIGSERIAL PRIMARY KEY,
    digest           TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    team_id          INTEGER NOT NULL CHECK (team_id > 0),
    attempt          TEXT NOT NULL CHECK (btrim(attempt) <> ''),
    lease_expires_at TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (lease_expires_at > created_at)
);

CREATE INDEX agent_snapshot_staged_uploads_expiry
    ON agent_snapshot_staged_uploads (lease_expires_at, id);
CREATE INDEX agent_snapshot_staged_uploads_digest
    ON agent_snapshot_staged_uploads (digest, lease_expires_at, id);

CREATE TABLE agent_workflow_runs (
    id                         BIGSERIAL PRIMARY KEY,
    team_id                    INTEGER NOT NULL CHECK (team_id > 0),
    team_name                  TEXT NOT NULL CHECK (btrim(team_name) <> ''),
    workflow_definition_id     INTEGER NOT NULL REFERENCES agent_workflow_definitions (id) ON DELETE RESTRICT,
    workflow_name              TEXT NOT NULL CHECK (btrim(workflow_name) <> ''),
    workflow_version           INTEGER NOT NULL CHECK (workflow_version > 0),
    schema_version             INTEGER NOT NULL CHECK (schema_version > 0),
    signature_version          INTEGER NOT NULL CHECK (signature_version > 0),
    definition_content_hash    TEXT NOT NULL CHECK (btrim(definition_content_hash) <> ''),
    function_id                TEXT CHECK (function_id IS NULL OR btrim(function_id) <> ''),
    idempotency_key            TEXT NOT NULL CHECK (btrim(idempotency_key) <> ''),
    parameterized_config       JSONB NOT NULL,
    parameterized_config_hash  TEXT NOT NULL CHECK (btrim(parameterized_config_hash) <> ''),
    concrete_config            JSONB,
    concrete_config_hash       TEXT,
    actual_plan                JSONB,
    actual_plan_hash           TEXT,
    resolved_dependencies      JSONB,
    origin_kind                TEXT NOT NULL CHECK (btrim(origin_kind) <> ''),
    origin_reference           TEXT NOT NULL,
    created_by                 TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    status                     TEXT NOT NULL CHECK (btrim(status) <> ''),
    error_message              TEXT NOT NULL DEFAULT '',
    retry_of_workflow_run_id   BIGINT REFERENCES agent_workflow_runs (id) ON DELETE SET NULL,
    pipeline_run_id            INTEGER REFERENCES pipeline_runs (id) ON DELETE SET NULL,
    template_pipeline_id       INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
    instance_pipeline_id       INTEGER REFERENCES pipelines (id) ON DELETE SET NULL,
    planned_build_id           INTEGER REFERENCES builds (id) ON DELETE SET NULL,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at                 TIMESTAMPTZ,
    completed_at               TIMESTAMPTZ,
    UNIQUE (team_id, idempotency_key),
    CHECK ((concrete_config IS NULL) = (concrete_config_hash IS NULL)),
    CHECK ((actual_plan IS NULL) = (actual_plan_hash IS NULL)),
    CHECK (completed_at IS NULL OR completed_at >= created_at)
);

CREATE INDEX agent_workflow_runs_team_workflow_created
    ON agent_workflow_runs (team_id, workflow_name, created_at DESC, id DESC);
CREATE INDEX agent_workflow_runs_team_status_created
    ON agent_workflow_runs (team_id, status, created_at, id);
CREATE INDEX agent_workflow_runs_origin
    ON agent_workflow_runs (team_id, origin_kind, origin_reference, created_at DESC, id DESC);

CREATE TABLE agent_snapshot_productions (
    id                         BIGSERIAL PRIMARY KEY,
    snapshot_id                BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    build_id                   BIGINT NOT NULL CHECK (build_id > 0),
    team_id                    INTEGER NOT NULL CHECK (team_id > 0),
    team_name                  TEXT NOT NULL CHECK (btrim(team_name) <> ''),
    created_by                 TEXT NOT NULL CHECK (btrim(created_by) <> ''),
    plan_id                    TEXT NOT NULL CHECK (btrim(plan_id) <> ''),
    attempt                    TEXT NOT NULL CHECK (btrim(attempt) <> ''),
    step_kind                  TEXT NOT NULL CHECK (btrim(step_kind) <> ''),
    step_name                  TEXT NOT NULL CHECK (btrim(step_name) <> ''),
    output_port                TEXT NOT NULL CHECK (btrim(output_port) <> ''),
    workflow_definition_id     INTEGER REFERENCES agent_workflow_definitions (id) ON DELETE SET NULL,
    workflow_run_id            BIGINT REFERENCES agent_workflow_runs (id) ON DELETE SET NULL,
    source_metadata            JSONB,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (build_id, plan_id, attempt, output_port)
);

CREATE INDEX agent_snapshot_productions_snapshot_created
    ON agent_snapshot_productions (snapshot_id, created_at, id);
CREATE INDEX agent_snapshot_productions_workflow_run
    ON agent_snapshot_productions (workflow_run_id, created_at, id)
    WHERE workflow_run_id IS NOT NULL;

CREATE TABLE agent_snapshot_lineage (
    production_id     BIGINT NOT NULL REFERENCES agent_snapshot_productions (id) ON DELETE CASCADE,
    position          INTEGER NOT NULL CHECK (position >= 0),
    input_port        TEXT NOT NULL CHECK (btrim(input_port) <> ''),
    input_snapshot_id BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    PRIMARY KEY (production_id, position),
    UNIQUE (production_id, input_port)
);

CREATE INDEX agent_snapshot_lineage_input_snapshot
    ON agent_snapshot_lineage (input_snapshot_id, production_id, position);

CREATE TABLE agent_snapshot_grants (
    id          BIGSERIAL PRIMARY KEY,
    snapshot_id BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    team_id     INTEGER NOT NULL CHECK (team_id > 0),
    granted_by  TEXT NOT NULL CHECK (btrim(granted_by) <> ''),
    reason      TEXT NOT NULL CHECK (btrim(reason) <> ''),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (snapshot_id, team_id)
);

CREATE INDEX agent_snapshot_grants_team_snapshot
    ON agent_snapshot_grants (team_id, snapshot_id);

CREATE TABLE agent_snapshot_retention_claims (
    id          BIGSERIAL PRIMARY KEY,
    snapshot_id BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    team_id     INTEGER NOT NULL CHECK (team_id > 0),
    class       TEXT NOT NULL CHECK (class IN ('binding', 'workflow', 'fixture', 'pin')),
    expires_at  TIMESTAMPTZ,
    actor       TEXT NOT NULL DEFAULT '',
    reason      TEXT NOT NULL CHECK (btrim(reason) <> ''),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (snapshot_id, team_id, class, actor)
);

CREATE INDEX agent_snapshot_retention_claims_snapshot_expiry
    ON agent_snapshot_retention_claims (snapshot_id, expires_at, id);
CREATE INDEX agent_snapshot_retention_claims_team
    ON agent_snapshot_retention_claims (team_id, snapshot_id, class, actor);

CREATE TABLE agent_workflow_run_snapshots (
    workflow_run_id BIGINT NOT NULL REFERENCES agent_workflow_runs (id) ON DELETE CASCADE,
    direction       TEXT NOT NULL CHECK (direction IN ('input', 'output')),
    port_name       TEXT NOT NULL CHECK (btrim(port_name) <> ''),
    snapshot_id     BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workflow_run_id, direction, port_name)
);

CREATE INDEX agent_workflow_run_snapshots_snapshot
    ON agent_workflow_run_snapshots (snapshot_id, workflow_run_id, direction);
