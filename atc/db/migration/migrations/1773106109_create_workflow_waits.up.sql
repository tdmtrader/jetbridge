CREATE TABLE agent_workflow_waits (
    id                     BIGSERIAL PRIMARY KEY,
    team_id                INTEGER NOT NULL CHECK (team_id > 0),
    workflow_run_id        BIGINT NOT NULL REFERENCES agent_workflow_runs (id) ON DELETE CASCADE,
    -- Builds are GC-owned. Keep a nullable live reference and an immutable
    -- copied identity so wait provenance remains readable after build GC.
    build_id               BIGINT REFERENCES builds (id) ON DELETE SET NULL,
    build_id_evidence      BIGINT NOT NULL CHECK (build_id_evidence > 0),
    plan_id                TEXT NOT NULL CHECK (btrim(plan_id) <> ''),
    attempt                TEXT NOT NULL CHECK (btrim(attempt) <> ''),
    output_name            TEXT NOT NULL CHECK (btrim(output_name) <> ''),
    question_name          TEXT NOT NULL CHECK (btrim(question_name) <> ''),
    question_snapshot_id   BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    expected_type_name     TEXT NOT NULL CHECK (btrim(expected_type_name) <> ''),
    expected_type_version  INTEGER NOT NULL CHECK (expected_type_version > 0),
    deadline               TIMESTAMPTZ NOT NULL,
    timeout_policy         TEXT NOT NULL CHECK (timeout_policy IN ('fail', 'default')),
    default_snapshot_id    BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    workflow_port          TEXT,
    workflow_definition_id INTEGER REFERENCES agent_workflow_definitions (id) ON DELETE RESTRICT,
    status                 TEXT NOT NULL DEFAULT 'waiting'
                           CHECK (status IN ('waiting', 'resolved', 'timed_out', 'cancelled')),
    answer_snapshot_id     BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    resolved_by            TEXT NOT NULL DEFAULT '',
    resolution_source      TEXT NOT NULL DEFAULT ''
                           CHECK (resolution_source IN ('', 'human', 'timeout', 'cancel')),
    resolved_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (build_id_evidence, plan_id, attempt, output_name),
    CHECK (build_id IS NULL OR build_id = build_id_evidence),
    CHECK (
        (timeout_policy = 'default' AND default_snapshot_id IS NOT NULL)
        OR (timeout_policy = 'fail' AND default_snapshot_id IS NULL)
    ),
    CHECK (
        (workflow_port IS NULL AND workflow_definition_id IS NULL)
        OR (workflow_port IS NOT NULL AND btrim(workflow_port) <> '' AND workflow_definition_id IS NOT NULL)
    ),
    CHECK (
        (status = 'waiting' AND answer_snapshot_id IS NULL AND resolved_by = ''
            AND resolution_source = '' AND resolved_at IS NULL)
        OR (status = 'resolved' AND answer_snapshot_id IS NOT NULL AND btrim(resolved_by) <> ''
            AND resolution_source = 'human' AND resolved_at IS NOT NULL)
        OR (status = 'timed_out' AND btrim(resolved_by) <> ''
            AND resolution_source = 'timeout' AND resolved_at IS NOT NULL
            AND ((timeout_policy = 'default' AND answer_snapshot_id = default_snapshot_id)
                OR (timeout_policy = 'fail' AND answer_snapshot_id IS NULL)))
        OR (status = 'cancelled' AND answer_snapshot_id IS NULL AND btrim(resolved_by) <> ''
            AND resolution_source = 'cancel' AND resolved_at IS NOT NULL)
    )
);

CREATE INDEX agent_workflow_waits_run_created
    ON agent_workflow_waits (workflow_run_id, created_at, id);

CREATE INDEX agent_workflow_waits_build
    ON agent_workflow_waits (build_id)
    WHERE build_id IS NOT NULL;

CREATE INDEX agent_workflow_waits_open_deadline
    ON agent_workflow_waits (deadline, id)
    WHERE status = 'waiting';

CREATE INDEX agent_workflow_waits_answer
    ON agent_workflow_waits (answer_snapshot_id, workflow_run_id)
    WHERE answer_snapshot_id IS NOT NULL;
