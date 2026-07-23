CREATE TABLE agent_experiments (
    id                         BIGSERIAL PRIMARY KEY,
    team_id                    INTEGER NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    team_name                  TEXT NOT NULL CHECK (btrim(team_name) = team_name AND team_name <> ''),
    name                       TEXT NOT NULL
                               CHECK (btrim(name) = name AND name <> '' AND octet_length(name) <= 128),
    state                      TEXT NOT NULL DEFAULT 'draft'
                               CHECK (state IN ('draft', 'running', 'canceling', 'canceled', 'completed', 'failed')),
    revision                   BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    candidate_signature        JSONB NOT NULL CHECK (jsonb_typeof(candidate_signature) = 'object'),
    repetitions                INTEGER NOT NULL CHECK (repetitions BETWEEN 1 AND 1000),
    per_cell_budget_usd        NUMERIC(18, 6) NOT NULL DEFAULT 0 CHECK (per_cell_budget_usd >= 0),
    total_budget_usd           NUMERIC(18, 6) NOT NULL DEFAULT 0 CHECK (total_budget_usd >= 0),
    max_tokens_per_cell        BIGINT NOT NULL DEFAULT 0 CHECK (max_tokens_per_cell >= 0),
    evaluator_target_kind      TEXT NOT NULL CHECK (evaluator_target_kind IN ('workflow', 'function')),
    evaluator_workflow_name    TEXT NOT NULL
                               CHECK (btrim(evaluator_workflow_name) = evaluator_workflow_name
                                      AND evaluator_workflow_name <> ''),
    evaluator_definition_id    INTEGER NOT NULL CHECK (evaluator_definition_id > 0),
    evaluator_workflow_version INTEGER NOT NULL CHECK (evaluator_workflow_version > 0),
    evaluator_function_id      TEXT,
    evaluator_signature        JSONB NOT NULL CHECK (jsonb_typeof(evaluator_signature) = 'object'),
    evaluator_target_config_hash TEXT CHECK (
                                   evaluator_target_config_hash IS NULL
                                   OR evaluator_target_config_hash ~ '^[0-9a-f]{64}$'
                               ),
    evaluator_measurements_port TEXT NOT NULL
                                CHECK (btrim(evaluator_measurements_port) = evaluator_measurements_port
                                       AND evaluator_measurements_port <> ''),
    created_by                 TEXT NOT NULL
                               CHECK (btrim(created_by) = created_by AND created_by <> ''
                                      AND octet_length(created_by) <= 256),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at                 TIMESTAMPTZ,
    completed_at               TIMESTAMPTZ,
    CHECK (
        (evaluator_target_kind = 'workflow' AND evaluator_function_id IS NULL)
        OR
        (evaluator_target_kind = 'function' AND evaluator_function_id IS NOT NULL
            AND btrim(evaluator_function_id) = evaluator_function_id
            AND evaluator_function_id <> '')
    ),
    CHECK ((state = 'draft' AND started_at IS NULL) OR (state <> 'draft' AND started_at IS NOT NULL)),
    CHECK ((state = 'draft') = (evaluator_target_config_hash IS NULL)),
    CHECK ((state IN ('canceled', 'completed', 'failed')) = (completed_at IS NOT NULL)),
    CHECK (started_at IS NULL OR started_at >= created_at),
    CHECK (completed_at IS NULL OR completed_at >= started_at)
);

CREATE INDEX agent_experiments_team_created
    ON agent_experiments (team_id, created_at DESC, id DESC);
CREATE INDEX agent_experiments_state_updated
    ON agent_experiments (state, updated_at, id);

CREATE TABLE agent_experiment_variants (
    id               BIGSERIAL PRIMARY KEY,
    experiment_id    BIGINT NOT NULL REFERENCES agent_experiments (id) ON DELETE CASCADE,
    label             TEXT NOT NULL
                      CHECK (btrim(label) = label AND label <> '' AND octet_length(label) <= 128),
    is_control        BOOLEAN NOT NULL DEFAULT false,
    target_kind       TEXT NOT NULL CHECK (target_kind IN ('workflow', 'function')),
    workflow_name     TEXT NOT NULL CHECK (btrim(workflow_name) = workflow_name AND workflow_name <> ''),
    definition_id     INTEGER NOT NULL CHECK (definition_id > 0),
    workflow_version  INTEGER NOT NULL CHECK (workflow_version > 0),
    function_id       TEXT,
    signature_hash    TEXT NOT NULL CHECK (signature_hash ~ '^[0-9a-f]{64}$'),
    target_config_hash TEXT CHECK (
                           target_config_hash IS NULL
                           OR target_config_hash ~ '^[0-9a-f]{64}$'
                       ),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (experiment_id, label),
    CHECK (
        (target_kind = 'workflow' AND function_id IS NULL)
        OR
        (target_kind = 'function' AND function_id IS NOT NULL
            AND btrim(function_id) = function_id AND function_id <> '')
    )
);

CREATE UNIQUE INDEX agent_experiment_variants_one_control
    ON agent_experiment_variants (experiment_id)
    WHERE is_control;

CREATE TABLE agent_experiment_fixtures (
    id            BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL REFERENCES agent_experiments (id) ON DELETE CASCADE,
    label         TEXT NOT NULL
                  CHECK (btrim(label) = label AND label <> '' AND octet_length(label) <= 128),
    role          TEXT NOT NULL CHECK (role IN ('normal', 'negative_control')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (experiment_id, label)
);

CREATE TABLE agent_experiment_fixture_bindings (
    fixture_id        BIGINT NOT NULL REFERENCES agent_experiment_fixtures (id) ON DELETE CASCADE,
    port_name         TEXT NOT NULL CHECK (btrim(port_name) = port_name AND port_name <> ''),
    snapshot_id       BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    retention_claim_id BIGINT NOT NULL
                       REFERENCES agent_snapshot_retention_claims (id) ON DELETE RESTRICT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (fixture_id, port_name),
    UNIQUE (retention_claim_id)
);

CREATE INDEX agent_experiment_fixture_bindings_snapshot
    ON agent_experiment_fixture_bindings (snapshot_id, fixture_id);

CREATE TABLE agent_experiment_control_assertions (
    fixture_id    BIGINT NOT NULL REFERENCES agent_experiment_fixtures (id) ON DELETE CASCADE,
    metric_name   TEXT NOT NULL CHECK (btrim(metric_name) = metric_name AND metric_name <> ''),
    comparator    TEXT NOT NULL CHECK (comparator IN ('lt', 'lte', 'gt', 'gte', 'between')),
    threshold_one DOUBLE PRECISION NOT NULL,
    threshold_two DOUBLE PRECISION,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (fixture_id, metric_name),
    CHECK (threshold_one NOT IN ('NaN'::double precision, 'Infinity'::double precision, '-Infinity'::double precision)),
    CHECK (threshold_two IS NULL OR threshold_two NOT IN ('NaN'::double precision, 'Infinity'::double precision, '-Infinity'::double precision)),
    CHECK (
        (comparator IN ('lt', 'lte', 'gt', 'gte') AND threshold_two IS NULL)
        OR
        (comparator = 'between' AND threshold_two IS NOT NULL AND threshold_one <= threshold_two)
    )
);

CREATE TABLE agent_experiment_evaluator_mappings (
    experiment_id    BIGINT NOT NULL REFERENCES agent_experiments (id) ON DELETE CASCADE,
    evaluator_port   TEXT NOT NULL CHECK (btrim(evaluator_port) = evaluator_port AND evaluator_port <> ''),
    source_direction TEXT NOT NULL CHECK (source_direction IN ('fixture_input', 'candidate_output')),
    source_port      TEXT NOT NULL CHECK (btrim(source_port) = source_port AND source_port <> ''),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (experiment_id, evaluator_port)
);

CREATE TABLE agent_experiment_cells (
    id                        BIGSERIAL PRIMARY KEY,
    experiment_id             BIGINT NOT NULL REFERENCES agent_experiments (id) ON DELETE CASCADE,
    fixture_id                BIGINT NOT NULL REFERENCES agent_experiment_fixtures (id) ON DELETE RESTRICT,
    variant_id                BIGINT NOT NULL REFERENCES agent_experiment_variants (id) ON DELETE RESTRICT,
    repetition                INTEGER NOT NULL CHECK (repetition > 0),
    status                    TEXT NOT NULL DEFAULT 'pending'
                              CHECK (status IN (
                                  'pending', 'running', 'valid_measurement',
                                  'candidate_contract_failure', 'candidate_platform_failure',
                                  'evaluator_failure', 'negative_control_assertion_failure',
                                  'skipped_budget', 'canceled'
                              )),
    candidate_workflow_run_id BIGINT REFERENCES agent_workflow_runs (id) ON DELETE RESTRICT,
    candidate_failure_category TEXT
                               CHECK (candidate_failure_category IS NULL OR candidate_failure_category IN (
                                   'budget_denied', 'invalid_admission', 'platform_failure'
                               )),
    lease_until               TIMESTAMPTZ,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at              TIMESTAMPTZ,
    UNIQUE (experiment_id, fixture_id, variant_id, repetition),
    CHECK (
        (status IN ('pending', 'running') AND completed_at IS NULL)
        OR
        (status NOT IN ('pending', 'running') AND completed_at IS NOT NULL)
    ),
    CHECK (candidate_workflow_run_id IS NULL OR candidate_workflow_run_id > 0),
    CHECK (lease_until IS NULL OR status IN ('pending', 'running'))
);

CREATE INDEX agent_experiment_cells_claimable
    ON agent_experiment_cells (lease_until, id)
    WHERE status IN ('pending', 'running');
CREATE INDEX agent_experiment_cells_experiment
    ON agent_experiment_cells (experiment_id, fixture_id, variant_id, repetition);

CREATE TABLE agent_experiment_evaluations (
    cell_id                   BIGINT PRIMARY KEY REFERENCES agent_experiment_cells (id) ON DELETE CASCADE,
    evaluator_workflow_run_id BIGINT REFERENCES agent_workflow_runs (id) ON DELETE RESTRICT
                                      CHECK (evaluator_workflow_run_id > 0),
    measurement_snapshot_id   BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    status                    TEXT
                              CHECK (status IS NULL OR status IN (
                                  'valid_measurement', 'candidate_contract_failure',
                                  'candidate_platform_failure', 'evaluator_failure',
                                  'negative_control_assertion_failure', 'skipped_budget', 'canceled'
                              )),
    measurements              JSONB CHECK (measurements IS NULL OR jsonb_typeof(measurements) = 'object'),
    created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at              TIMESTAMPTZ,
    CHECK ((status IS NULL) = (completed_at IS NULL)),
    CHECK (measurement_snapshot_id IS NULL OR measurements IS NOT NULL)
);

CREATE INDEX agent_experiment_evaluations_measurement
    ON agent_experiment_evaluations (measurement_snapshot_id, cell_id)
    WHERE measurement_snapshot_id IS NOT NULL;
