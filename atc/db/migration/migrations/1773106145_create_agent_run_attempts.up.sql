CREATE TABLE agent_run_attempts (
    id BIGSERIAL PRIMARY KEY,
    head_id BIGINT NOT NULL
        REFERENCES agent_run_checkpoint_heads (id) ON DELETE CASCADE,
    attempt_number INTEGER NOT NULL CHECK (attempt_number > 0),
    max_total_attempts INTEGER NOT NULL DEFAULT 3
        CHECK (max_total_attempts > 0),
    state TEXT NOT NULL DEFAULT 'scheduling'
        CHECK (state IN (
            'scheduling',
            'materializing',
            'running',
            'interrupted',
            'finalizing',
            'succeeded',
            'manual_review_required'
        )),
    is_current BOOLEAN NOT NULL DEFAULT TRUE,
    materialization_id TEXT NOT NULL
        CHECK (
            btrim(materialization_id) <> ''
            AND octet_length(materialization_id) <= 256
        ),
    source_attempt_number INTEGER,
    source_checkpoint_id BIGINT,
    source_checkpoint_generation INTEGER NOT NULL DEFAULT 0,
    recovery_mode TEXT
        CHECK (recovery_mode IN (
            'native_resume',
            'workspace_only',
            'checkpoint_zero'
        )),
    source_interruption_reason TEXT
        CHECK (source_interruption_reason IN (
            'pod_deleted',
            'evicted',
            'node_lost',
            'preempted'
        )),
    interruption_reason TEXT
        CHECK (interruption_reason IN (
            'pod_deleted',
            'evicted',
            'node_lost',
            'preempted'
        )),
    fence_token UUID,
    fence_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    interrupted_at TIMESTAMPTZ,
    terminal_at TIMESTAMPTZ,
    UNIQUE (head_id, attempt_number),
    CHECK (attempt_number <= max_total_attempts),
    CHECK (
        (attempt_number = 1
            AND source_attempt_number IS NULL
            AND source_checkpoint_id IS NULL
            AND source_checkpoint_generation = 0
            AND recovery_mode IS NULL
            AND source_interruption_reason IS NULL)
        OR
        (attempt_number > 1
            AND source_attempt_number > 0
            AND source_attempt_number < attempt_number
            AND recovery_mode IS NOT NULL
            AND source_interruption_reason IS NOT NULL)
    ),
    CHECK (
        (recovery_mode IN ('native_resume', 'workspace_only')
            AND source_checkpoint_id > 0
            AND source_checkpoint_generation > 0)
        OR
        (recovery_mode = 'checkpoint_zero'
            AND source_checkpoint_id IS NULL
            AND source_checkpoint_generation = 0)
        OR
        recovery_mode IS NULL
    ),
    CHECK (fence_expires_at IS NULL OR fence_token IS NOT NULL),
    CHECK (is_current OR fence_expires_at IS NULL),
    CHECK (
        state NOT IN ('interrupted', 'succeeded', 'manual_review_required')
        OR fence_expires_at IS NULL
    ),
    CHECK (interruption_reason IS NULL OR interrupted_at IS NOT NULL),
    CHECK (state <> 'interrupted' OR (
        interruption_reason IS NOT NULL
        AND interrupted_at IS NOT NULL
    )),
    CHECK (
        (state IN ('succeeded', 'manual_review_required') AND terminal_at IS NOT NULL)
        OR
        (state NOT IN ('succeeded', 'manual_review_required') AND terminal_at IS NULL)
    )
);

-- Tokens are caller-generated capabilities. Keep a compact immutable issuance
-- ledger so a released or expired capability can never be recycled after a
-- later lease generation.
CREATE TABLE agent_run_attempt_fence_tokens (
    token UUID PRIMARY KEY,
    attempt_id BIGINT
        REFERENCES agent_run_attempts (id) ON DELETE SET NULL,
    issued_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX agent_run_attempt_fence_tokens_attempt
    ON agent_run_attempt_fence_tokens (attempt_id);

CREATE UNIQUE INDEX agent_run_attempts_one_current
    ON agent_run_attempts (head_id)
    WHERE is_current;

CREATE UNIQUE INDEX agent_run_attempts_one_replacement
    ON agent_run_attempts (head_id, source_attempt_number)
    WHERE source_attempt_number IS NOT NULL;

CREATE INDEX agent_run_attempts_current_lookup
    ON agent_run_attempts (head_id, attempt_number)
    WHERE is_current;

CREATE FUNCTION enforce_agent_run_attempt_insertion()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    source_max_total_attempts INTEGER;
    source_state TEXT;
    source_is_current BOOLEAN;
    source_reason TEXT;
BEGIN
    IF NEW.attempt_number = 1 THEN
        RETURN NEW;
    END IF;

    SELECT max_total_attempts, state, is_current, interruption_reason
    INTO source_max_total_attempts, source_state, source_is_current, source_reason
    FROM agent_run_attempts
    WHERE head_id = NEW.head_id
      AND attempt_number = NEW.source_attempt_number
    FOR SHARE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'a recovery attempt requires its durable source attempt';
    END IF;

    IF NEW.attempt_number <> NEW.source_attempt_number + 1 THEN
        RAISE EXCEPTION 'a recovery attempt must immediately follow its source';
    END IF;

    IF NEW.max_total_attempts <> source_max_total_attempts THEN
        RAISE EXCEPTION 'a recovery attempt cannot change the durable attempt maximum';
    END IF;

    IF source_state <> 'interrupted' OR source_is_current THEN
        RAISE EXCEPTION 'a recovery source must be interrupted and superseded';
    END IF;

    IF NEW.source_interruption_reason IS DISTINCT FROM source_reason THEN
        RAISE EXCEPTION 'a recovery reason must match its interrupted source';
    END IF;

    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_run_attempts_insertion
BEFORE INSERT ON agent_run_attempts
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_run_attempt_insertion();

CREATE FUNCTION enforce_agent_run_attempt_transition()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.head_id <> OLD.head_id
       OR NEW.attempt_number <> OLD.attempt_number
       OR NEW.max_total_attempts <> OLD.max_total_attempts
       OR NEW.materialization_id <> OLD.materialization_id
       OR NEW.source_attempt_number IS DISTINCT FROM OLD.source_attempt_number
       OR NEW.source_checkpoint_id IS DISTINCT FROM OLD.source_checkpoint_id
       OR NEW.source_checkpoint_generation <> OLD.source_checkpoint_generation
       OR NEW.recovery_mode IS DISTINCT FROM OLD.recovery_mode
       OR NEW.source_interruption_reason IS DISTINCT FROM OLD.source_interruption_reason
       OR NEW.created_at <> OLD.created_at THEN
        RAISE EXCEPTION 'agent run attempt provenance is immutable';
    END IF;

    IF OLD.is_current = FALSE AND NEW.is_current = TRUE THEN
        RAISE EXCEPTION 'a superseded execution attempt cannot become current';
    END IF;

    IF NEW.state <> OLD.state AND NOT (
        (OLD.state = 'scheduling' AND NEW.state IN (
            'materializing', 'running', 'interrupted', 'manual_review_required'
        ))
        OR (OLD.state = 'materializing' AND NEW.state IN (
            'running', 'interrupted', 'manual_review_required'
        ))
        OR (OLD.state = 'running' AND NEW.state IN (
            'interrupted', 'finalizing', 'manual_review_required'
        ))
        OR (OLD.state = 'interrupted' AND NEW.state = 'manual_review_required')
        OR (OLD.state = 'finalizing' AND NEW.state IN (
            'interrupted', 'succeeded', 'manual_review_required'
        ))
    ) THEN
        RAISE EXCEPTION 'invalid agent run attempt state transition: % -> %',
            OLD.state, NEW.state;
    END IF;

    IF NEW.state = 'interrupted' AND NEW.fence_expires_at IS NOT NULL THEN
        RAISE EXCEPTION 'an interrupted execution attempt cannot retain a live fence';
    END IF;

    -- A token identifies one lease generation forever. Exact acquisition
    -- retries return the existing row; they never extend it. Once released or
    -- expired, the same token cannot reacquire authority.
    IF NEW.fence_token = OLD.fence_token
       AND NEW.fence_expires_at IS DISTINCT FROM OLD.fence_expires_at
       AND NOT (
           OLD.fence_expires_at IS NOT NULL
           AND NEW.fence_expires_at IS NULL
       ) THEN
        RAISE EXCEPTION 'an attempt fence token cannot be renewed';
    END IF;

    IF NEW.fence_token IS DISTINCT FROM OLD.fence_token
       AND (
           NEW.is_current = FALSE
           OR NEW.state IN ('succeeded', 'manual_review_required')
           OR NEW.fence_expires_at IS NULL
       ) THEN
        RAISE EXCEPTION 'a new attempt fence requires current nonterminal authority';
    END IF;

    NEW.updated_at = clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_run_attempts_transition
BEFORE UPDATE ON agent_run_attempts
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_run_attempt_transition();
