CREATE TABLE agent_publications (
    id                       BIGSERIAL PRIMARY KEY,
    operation_key            TEXT NOT NULL UNIQUE
                               CHECK (operation_key ~ '^sha256:[0-9a-f]{64}$'),
    team_id                  INTEGER NOT NULL CHECK (team_id > 0),
    team_name                TEXT NOT NULL
                               CHECK (btrim(team_name) = team_name AND team_name <> ''
                                      AND octet_length(team_name) <= 256),
    workflow_run_id          BIGINT NOT NULL,
    build_id                 BIGINT NOT NULL CHECK (build_id > 0),
    actor                    TEXT NOT NULL
                               CHECK (btrim(actor) = actor AND actor <> ''
                                      AND octet_length(actor) <= 256),
    input_snapshot_id        BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    publisher                TEXT NOT NULL CHECK (btrim(publisher) <> ''),
    destination              TEXT NOT NULL CHECK (btrim(destination) = destination AND destination <> ''),
    mode                     TEXT NOT NULL
                               CHECK (mode IN ('branch', 'pull-request', 'merge', 'comment', 'state')),
    parameters               JSONB NOT NULL DEFAULT '{}'::jsonb
                               CHECK (jsonb_typeof(parameters) = 'object'),
    approval_policy_version  TEXT NOT NULL
                               CHECK (btrim(approval_policy_version) = approval_policy_version
                                      AND approval_policy_version <> ''),
    approved_by              TEXT,
    approval_wait_id         BIGINT REFERENCES agent_workflow_waits (id) ON DELETE RESTRICT,
    approval_question_snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    approval_answer_snapshot_id   BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    approval_resolved_at     TIMESTAMPTZ,
    status                   TEXT NOT NULL DEFAULT 'pending'
                               CHECK (status IN ('pending', 'succeeded', 'failed', 'stale_base', 'rebase_required')),
    attempt                  INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0),
    lease_until              TIMESTAMPTZ,
    result                   JSONB NOT NULL DEFAULT '{}'::jsonb
                               CHECK (jsonb_typeof(result) = 'object'),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id) ON DELETE RESTRICT,
    CHECK (
        (mode = 'merge' AND approved_by IS NOT NULL AND btrim(approved_by) <> ''
            AND approval_wait_id IS NOT NULL
            AND approval_question_snapshot_id IS NOT NULL
            AND approval_answer_snapshot_id IS NOT NULL
            AND approval_resolved_at IS NOT NULL)
        OR (mode <> 'merge' AND approved_by IS NULL
            AND approval_wait_id IS NULL
            AND approval_question_snapshot_id IS NULL
            AND approval_answer_snapshot_id IS NULL
            AND approval_resolved_at IS NULL)
    ),
    CHECK (
        (status = 'pending' AND lease_until IS NOT NULL AND result = '{}'::jsonb)
        OR (status <> 'pending' AND lease_until IS NULL AND result <> '{}'::jsonb)
    )
);

CREATE INDEX agent_publications_input_snapshot
    ON agent_publications (input_snapshot_id, created_at, id);

CREATE INDEX agent_publications_team_created
    ON agent_publications (team_id, created_at, id);

CREATE INDEX agent_publications_workflow_run
    ON agent_publications (workflow_run_id, created_at, id);

CREATE INDEX agent_publications_approval_wait
    ON agent_publications (approval_wait_id, created_at, id)
    WHERE approval_wait_id IS NOT NULL;

CREATE INDEX agent_publications_pending_lease
    ON agent_publications (lease_until, id)
    WHERE status = 'pending';

CREATE INDEX agent_workflow_runs_publication_build
    ON agent_workflow_runs (planned_build_id, team_id)
    WHERE planned_build_id IS NOT NULL;
