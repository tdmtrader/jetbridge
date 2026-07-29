-- A recovery creates a fresh provider process whose cumulative counters start
-- at zero. Keep its immutable accounting record on the durable recovery
-- attempt, while agent_run_metrics remains the backwards-compatible aggregate
-- keyed by (build_id, plan_id).
CREATE TABLE agent_run_attempt_metrics (
    attempt_id BIGINT PRIMARY KEY
        REFERENCES agent_run_attempts (id) ON DELETE RESTRICT,
    build_id INTEGER NOT NULL,
    plan_id TEXT NOT NULL,
    execution_attempt INTEGER NOT NULL CHECK (execution_attempt > 0),
    workflow_run_id BIGINT,
    function_id TEXT NOT NULL,
    step_name TEXT NOT NULL,

    -- Server-owned accounting attribution. No recorder payload can choose
    -- these durable dimensions.
    user_id INTEGER,
    user_name TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL CHECK (source IN ('agent_step', 'ci_agent')),
    provider TEXT NOT NULL CHECK (provider <> ''),
    model TEXT NOT NULL CHECK (model <> ''),

    status TEXT NOT NULL CHECK (status IN ('ok', 'failed', 'error', 'incomplete')),
    summary TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cache_read_tokens BIGINT NOT NULL DEFAULT 0,
    cache_creation_tokens BIGINT NOT NULL DEFAULT 0,
    turns INTEGER NOT NULL DEFAULT 0,
    wall_time_seconds INTEGER NOT NULL DEFAULT 0,
    cost_usd NUMERIC(12,6) NOT NULL DEFAULT 0,
    results JSONB,
    events_artifact TEXT NOT NULL DEFAULT '',
    event_counts JSONB NOT NULL DEFAULT '{}'::jsonb,
    display_finalized BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),

    UNIQUE (build_id, plan_id, execution_attempt),
    CHECK (jsonb_typeof(event_counts) = 'object'),
    CHECK (input_tokens >= 0 AND output_tokens >= 0
           AND cache_read_tokens >= 0 AND cache_creation_tokens >= 0
           AND turns >= 0 AND wall_time_seconds >= 0 AND cost_usd >= 0)
);

CREATE INDEX agent_run_attempt_metrics_workflow_run
    ON agent_run_attempt_metrics (workflow_run_id, created_at)
    WHERE workflow_run_id IS NOT NULL;

CREATE UNIQUE INDEX agent_run_attempt_metrics_one_final_presentation
    ON agent_run_attempt_metrics (build_id, plan_id)
    WHERE display_finalized;

-- Make the copied lookup fields a verified view of the durable attempt. This
-- closes the direct-SQL escape hatch as well as the factory's server-only API.
CREATE FUNCTION enforce_agent_run_attempt_metric_binding()
RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    expected_build_id BIGINT;
    expected_plan_id TEXT;
    expected_execution_attempt INTEGER;
    expected_function_id TEXT;
    expected_workflow_run_id BIGINT;
BEGIN
    SELECT h.build_id, h.plan_id, a.attempt_number, h.function_id,
           h.workflow_run_provenance_id
    INTO expected_build_id, expected_plan_id, expected_execution_attempt,
         expected_function_id, expected_workflow_run_id
    FROM agent_run_attempts a
    JOIN agent_run_checkpoint_heads h ON h.id = a.head_id
    WHERE a.id = NEW.attempt_id;

    IF NOT FOUND OR NEW.build_id <> expected_build_id
       OR NEW.plan_id <> expected_plan_id
       OR NEW.execution_attempt <> expected_execution_attempt
       OR NEW.function_id <> expected_function_id
       OR NEW.workflow_run_id IS DISTINCT FROM expected_workflow_run_id THEN
        RAISE EXCEPTION 'agent run attempt metrics must bind the exact durable attempt identity';
    END IF;

    IF TG_OP = 'UPDATE' AND (
        NEW.attempt_id <> OLD.attempt_id
        OR NEW.build_id <> OLD.build_id
        OR NEW.plan_id <> OLD.plan_id
        OR NEW.execution_attempt <> OLD.execution_attempt
        OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
        OR NEW.function_id <> OLD.function_id
        OR NEW.step_name <> OLD.step_name
        OR NEW.user_id IS DISTINCT FROM OLD.user_id
        OR NEW.user_name <> OLD.user_name
        OR NEW.source <> OLD.source
        OR NEW.provider <> OLD.provider
        OR NEW.model <> OLD.model
    ) THEN
        RAISE EXCEPTION 'agent run attempt metric identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_run_attempt_metrics_bound_identity
BEFORE INSERT OR UPDATE ON agent_run_attempt_metrics
FOR EACH ROW EXECUTE FUNCTION enforce_agent_run_attempt_metric_binding();
