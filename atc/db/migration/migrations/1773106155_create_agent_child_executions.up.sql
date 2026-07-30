-- Child executions are a distinct durable authority from primary agent
-- attempts. Prompt text, provider credentials, and provider response bodies do
-- not belong in this ledger; it stores their immutable digests and protected
-- object references instead.
ALTER TABLE agent_snapshots
    ADD CONSTRAINT agent_snapshots_id_team_key UNIQUE (id, team_id);

CREATE TABLE agent_child_executions (
    id UUID PRIMARY KEY,
    team_id INTEGER NOT NULL
        REFERENCES teams (id) ON DELETE RESTRICT,
    workflow_run_id BIGINT NOT NULL,
    node_plan_id TEXT NOT NULL
        CHECK (btrim(node_plan_id) <> '' AND octet_length(node_plan_id) <= 512),
    parent_attempt INTEGER NOT NULL CHECK (parent_attempt > 0),
    idempotency_key TEXT NOT NULL
        CHECK (btrim(idempotency_key) <> '' AND octet_length(idempotency_key) <= 512),
    identity_digest TEXT NOT NULL
        CHECK (identity_digest ~ '^sha256:[0-9a-f]{64}$'),
    tool TEXT NOT NULL
        CHECK (tool IN ('request_review', 'consult_agent')),
    tier TEXT NOT NULL
        CHECK (tier IN ('economy', 'balanced', 'frontier')),
    effort TEXT NOT NULL
        CHECK (effort IN ('medium', 'high')),
    profile_id TEXT NOT NULL
        CHECK (btrim(profile_id) <> '' AND octet_length(profile_id) <= 256),
    profile_digest TEXT NOT NULL
        CHECK (profile_digest ~ '^sha256:[0-9a-f]{64}$'),
    input_digest TEXT NOT NULL
        CHECK (input_digest ~ '^sha256:[0-9a-f]{64}$'),
    attachments JSONB NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(attachments) = 'array'),
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN (
            'pending', 'admitted', 'capturing', 'running', 'validating',
            'sealing', 'succeeded', 'errored', 'cancelled', 'timed_out'
        )),
    sequence BIGINT NOT NULL DEFAULT 0 CHECK (sequence >= 0),
    broker_instance TEXT
        CHECK (broker_instance IS NULL OR (
            btrim(broker_instance) <> '' AND octet_length(broker_instance) <= 512
        )),
    lease_expires_at TIMESTAMPTZ,
    transcript_object TEXT
        CHECK (transcript_object IS NULL OR btrim(transcript_object) <> ''),
    result_snapshot_id BIGINT,
    observed_usage JSONB,
    duration_ms BIGINT CHECK (duration_ms IS NULL OR duration_ms >= 0),
    error_code TEXT
        CHECK (error_code IS NULL OR (
            error_code ~ '^[a-z][a-z0-9_]*$' AND octet_length(error_code) <= 128
        )),
    error_retryable BOOLEAN,
    error_summary TEXT
        CHECK (error_summary IS NULL OR octet_length(error_summary) <= 4096),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    terminal_at TIMESTAMPTZ,
    UNIQUE (workflow_run_id, node_plan_id, parent_attempt, idempotency_key),
    FOREIGN KEY (workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id) ON DELETE CASCADE,
    FOREIGN KEY (result_snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id) ON DELETE RESTRICT,
    CHECK (lease_expires_at IS NULL OR state NOT IN (
        'succeeded', 'errored', 'cancelled', 'timed_out'
    )),
    CHECK (
        (state = 'succeeded'
            AND result_snapshot_id IS NOT NULL
            AND terminal_at IS NOT NULL
            AND error_code IS NULL)
        OR
        (state IN ('errored', 'cancelled', 'timed_out')
            AND result_snapshot_id IS NULL
            AND terminal_at IS NOT NULL
            AND error_code IS NOT NULL
            AND error_retryable IS NOT NULL)
        OR
        (state NOT IN ('succeeded', 'errored', 'cancelled', 'timed_out')
            AND terminal_at IS NULL
            AND result_snapshot_id IS NULL
            AND error_code IS NULL
            AND error_retryable IS NULL
            AND error_summary IS NULL)
    )
);

CREATE INDEX agent_child_executions_team_created
    ON agent_child_executions (team_id, created_at DESC, id);
CREATE INDEX agent_child_executions_reconcile
    ON agent_child_executions (lease_expires_at, id)
    WHERE state NOT IN ('succeeded', 'errored', 'cancelled', 'timed_out');

CREATE TABLE agent_child_execution_events (
    execution_id UUID NOT NULL
        REFERENCES agent_child_executions (id) ON DELETE CASCADE,
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    state TEXT NOT NULL,
    phase TEXT NOT NULL
        CHECK (phase ~ '^[a-z][a-z0-9_]*$' AND octet_length(phase) <= 128),
    detail JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (execution_id, sequence)
);

CREATE FUNCTION enforce_agent_child_execution_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.team_id <> OLD.team_id
       OR NEW.workflow_run_id <> OLD.workflow_run_id
       OR NEW.node_plan_id <> OLD.node_plan_id
       OR NEW.parent_attempt <> OLD.parent_attempt
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.identity_digest <> OLD.identity_digest
       OR NEW.tool <> OLD.tool
       OR NEW.tier <> OLD.tier
       OR NEW.effort <> OLD.effort
       OR NEW.profile_id <> OLD.profile_id
       OR NEW.profile_digest <> OLD.profile_digest
       OR NEW.input_digest <> OLD.input_digest
       OR NEW.attachments <> OLD.attachments
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'agent child execution identity is immutable';
    END IF;

    IF NEW.sequence <> OLD.sequence + 1 THEN
        RAISE EXCEPTION 'agent child execution sequence must advance exactly once';
    END IF;

    IF NEW.state <> OLD.state AND NOT (
        (OLD.state = 'pending' AND NEW.state IN ('admitted', 'errored', 'cancelled'))
        OR (OLD.state = 'admitted' AND NEW.state IN (
            'capturing', 'running', 'errored', 'cancelled', 'timed_out'
        ))
        OR (OLD.state = 'capturing' AND NEW.state IN (
            'running', 'errored', 'cancelled', 'timed_out'
        ))
        OR (OLD.state = 'running' AND NEW.state IN (
            'validating', 'errored', 'cancelled', 'timed_out'
        ))
        OR (OLD.state = 'validating' AND NEW.state IN (
            'sealing', 'errored', 'cancelled', 'timed_out'
        ))
        OR (OLD.state = 'sealing' AND NEW.state IN (
            'succeeded', 'errored', 'timed_out'
        ))
    ) THEN
        RAISE EXCEPTION 'invalid agent child execution state transition: % -> %',
            OLD.state, NEW.state;
    END IF;

    NEW.updated_at = clock_timestamp();
    IF NEW.state IN ('succeeded', 'errored', 'cancelled', 'timed_out') THEN
        NEW.terminal_at = COALESCE(NEW.terminal_at, clock_timestamp());
        NEW.lease_expires_at = NULL;
    ELSIF OLD.terminal_at IS NOT NULL THEN
        RAISE EXCEPTION 'terminal agent child execution is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_child_executions_transition
BEFORE UPDATE ON agent_child_executions
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_child_execution_transition();
