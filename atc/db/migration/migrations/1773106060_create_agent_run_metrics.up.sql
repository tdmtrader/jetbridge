CREATE TABLE agent_run_metrics (
    id                    BIGSERIAL PRIMARY KEY,
    ticket_id             INTEGER,                 -- NULL for pure-CI agent steps
    pipeline_run_id       INTEGER,
    build_id              INTEGER NOT NULL,
    plan_id               TEXT NOT NULL DEFAULT '',    -- atc plan ID of the step (unique within build)
    step_name             TEXT NOT NULL,
    workflow_name         TEXT NOT NULL DEFAULT '',
    workflow_version      INTEGER,
    workflow_hash         TEXT NOT NULL DEFAULT '',    -- content_hash frozen at render time
    status                TEXT NOT NULL CHECK (status IN ('ok','failed','error')),
    summary               TEXT NOT NULL DEFAULT '',
    model                 TEXT NOT NULL DEFAULT '',
    input_tokens          BIGINT NOT NULL DEFAULT 0,
    output_tokens         BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens     BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns                 INTEGER NOT NULL DEFAULT 0,
    wall_time_seconds     INTEGER NOT NULL DEFAULT 0,
    cost_usd              NUMERIC(12,6) NOT NULL DEFAULT 0,
    results               JSONB,                   -- full results.json payload
    events_artifact       TEXT NOT NULL DEFAULT '',-- artifact-fabric handle for events.ndjson
    event_counts          JSONB,                   -- {"tool.call": 87, "subagent.call": 3, ...}
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_run_metrics_build_plan ON agent_run_metrics (build_id, plan_id);
CREATE INDEX agent_run_metrics_ticket   ON agent_run_metrics (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_run_metrics_workflow ON agent_run_metrics (workflow_name, workflow_version);
