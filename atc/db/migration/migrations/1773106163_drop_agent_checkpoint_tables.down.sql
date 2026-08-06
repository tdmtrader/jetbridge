-- Recreate the agent checkpoint schema dropped by 1773106160.
--
-- This is a STRUCTURAL restore only. The up migration refuses to run unless
-- every one of these tables is empty, so on any deployment that reached
-- 1773106160 there was no checkpoint data to lose -- but if you are reading
-- this after restoring a backup, note that rows are not coming back from here.
--
-- The DDL below is a verbatim replay of migrations 1773106144-47, which are
-- left in place rather than amended. Keep it that way: if those migrations
-- ever change, this file must be regenerated from them, not hand-edited.
--
-- Nothing in the tree reads these tables any more. They exist after a
-- downgrade so that the migration chain is continuous back to v8.0.1, not
-- because the checkpoint subsystem returns with them; that code was removed
-- in the same change and must be reverted separately.


-- ---------- replayed from 1773106144_create_agent_run_checkpoints_and_events.up.sql ----------

-- Checkpoint recovery is intentionally separate from semantic snapshots. A
-- committed manifest is PostgreSQL's recovery authority; the immutable bytes
-- it names remain Hangar's authority.
CREATE TABLE agent_run_checkpoint_heads (
    id BIGSERIAL PRIMARY KEY,
    -- Provenance is immutable copied v3 identity. The live FK is convenient
    -- while the workflow run exists, but must null on its deletion without
    -- erasing recovery attribution or involving checkpoint terminalization.
    workflow_run_provenance_id BIGINT,
    workflow_run_id BIGINT REFERENCES agent_workflow_runs(id) ON DELETE SET NULL,
    build_id BIGINT NOT NULL,
    plan_id TEXT NOT NULL,
    function_id TEXT NOT NULL,
    latest_checkpoint_id BIGINT,
    next_generation INTEGER NOT NULL DEFAULT 1 CHECK (next_generation > 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    terminal_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (build_id, plan_id, function_id),
    CHECK (workflow_run_id IS NULL OR workflow_run_id = workflow_run_provenance_id)
);

CREATE OR REPLACE FUNCTION enforce_agent_run_checkpoint_head_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.workflow_run_provenance_id IS DISTINCT FROM OLD.workflow_run_provenance_id
       OR NEW.build_id <> OLD.build_id
       OR NEW.plan_id <> OLD.plan_id
       OR NEW.function_id <> OLD.function_id THEN
        RAISE EXCEPTION 'agent run checkpoint head identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_run_checkpoint_heads_immutable_identity
BEFORE UPDATE ON agent_run_checkpoint_heads
FOR EACH ROW EXECUTE FUNCTION enforce_agent_run_checkpoint_head_identity();

CREATE TABLE agent_checkpoint_objects (
    id BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind = 'checkpoints'),
    digest TEXT NOT NULL,
    object_key TEXT NOT NULL,
    generation BIGINT CHECK (generation > 0),
    status TEXT NOT NULL DEFAULT 'uploading'
      CHECK (status IN ('uploading', 'available', 'deleting')),
    upload_token UUID,
    upload_expires_at TIMESTAMPTZ,
    delete_token UUID,
    delete_lease_expires_at TIMESTAMPTZ,
    reconciliation_token UUID,
    reconciliation_lease_expires_at TIMESTAMPTZ,
    missing_observed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (kind, digest, object_key),
    CHECK ((status = 'uploading') =
           (upload_token IS NOT NULL AND upload_expires_at IS NOT NULL)),
    CHECK ((status IN ('available', 'deleting')) = (generation IS NOT NULL)),
    CHECK ((status = 'deleting') = (delete_token IS NOT NULL)),
    CHECK ((delete_token IS NULL) = (delete_lease_expires_at IS NULL)),
    CHECK ((reconciliation_token IS NULL) =
           (reconciliation_lease_expires_at IS NULL)),
    CHECK (reconciliation_token IS NULL OR status = 'uploading'),
    CHECK (status <> 'deleting' OR reconciliation_token IS NULL)
);

CREATE TABLE agent_run_checkpoints (
    id BIGSERIAL PRIMARY KEY,
    head_id BIGINT NOT NULL REFERENCES agent_run_checkpoint_heads(id) ON DELETE RESTRICT,
    archive_object_id BIGINT REFERENCES agent_checkpoint_objects(id) ON DELETE RESTRICT,
    generation INTEGER NOT NULL CHECK (generation > 0),
    expected_previous_generation INTEGER NOT NULL CHECK (expected_previous_generation >= 0),
    execution_attempt INTEGER NOT NULL CHECK (execution_attempt > 0),
    fence_token UUID NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('staged', 'committed', 'superseded', 'aborted', 'expired')),
    manifest JSONB NOT NULL,
    stage_expires_at TIMESTAMPTZ NOT NULL,
    superseded_at TIMESTAMPTZ,
    aborted_at TIMESTAMPTZ,
    committed_at TIMESTAMPTZ,
    expired_at TIMESTAMPTZ,
    expiration_token UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (head_id, generation)
);

CREATE UNIQUE INDEX agent_run_checkpoints_one_committed
ON agent_run_checkpoints (head_id)
WHERE status = 'committed';

CREATE UNIQUE INDEX agent_run_checkpoints_one_staged
ON agent_run_checkpoints (head_id)
WHERE status = 'staged';

ALTER TABLE agent_run_checkpoint_heads
ADD CONSTRAINT agent_run_checkpoint_heads_latest
FOREIGN KEY (latest_checkpoint_id) REFERENCES agent_run_checkpoints(id)
DEFERRABLE INITIALLY DEFERRED;

CREATE TABLE agent_run_effects (
    id BIGSERIAL PRIMARY KEY,
    head_id BIGINT NOT NULL REFERENCES agent_run_checkpoint_heads(id) ON DELETE RESTRICT,
    execution_attempt INTEGER NOT NULL CHECK (execution_attempt > 0),
    tool_call_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    provider TEXT NOT NULL,
    adapter_version TEXT NOT NULL,
    fence_token UUID NOT NULL,
    read_only BOOLEAN NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    idempotency_contract TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('begun', 'committed')),
    begun_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    committed_at TIMESTAMPTZ,
    UNIQUE (head_id, execution_attempt, tool_call_id),
    CHECK ((state = 'committed') = (committed_at IS NOT NULL))
);

CREATE TABLE agent_run_events (
    id BIGSERIAL PRIMARY KEY,
    head_id BIGINT NOT NULL REFERENCES agent_run_checkpoint_heads(id) ON DELETE RESTRICT,
    execution_attempt INTEGER NOT NULL CHECK (execution_attempt > 0),
    event_type TEXT NOT NULL,
    reason TEXT NOT NULL DEFAULT '',
    checkpoint_generation INTEGER CHECK (checkpoint_generation IS NULL OR checkpoint_generation > 0),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Event/effect deletion is authorized by durable retention state, never a
-- caller-controlled session setting. The same predicate is used by bounded
-- cleanup while the factory holds the head row lock.
CREATE OR REPLACE FUNCTION agent_run_checkpoint_head_cleanup_eligible(candidate_head_id BIGINT)
RETURNS BOOLEAN AS $$
    SELECT EXISTS (
        SELECT 1
        FROM agent_run_checkpoint_heads h
        WHERE h.id = candidate_head_id
          AND h.active = FALSE
          AND h.terminal_at IS NOT NULL
          AND h.terminal_at <= now() - INTERVAL '30 days'
          AND h.latest_checkpoint_id IS NULL
          AND NOT EXISTS (
              SELECT 1 FROM agent_run_checkpoints c
              WHERE c.head_id = h.id AND c.status IN ('staged', 'committed')
          )
          AND NOT EXISTS (
              SELECT 1 FROM agent_run_checkpoints c
              WHERE c.head_id = h.id AND c.archive_object_id IS NOT NULL
          )
    );
$$ LANGUAGE SQL STABLE;

-- Effects are a state machine, not a mutable cache.
CREATE OR REPLACE FUNCTION enforce_agent_run_effect_transition() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF agent_run_checkpoint_head_cleanup_eligible(OLD.head_id) THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'agent run effects may only be removed by bounded retention cleanup';
    END IF;

    IF OLD.state <> 'begun' OR NEW.state <> 'committed' THEN
        RAISE EXCEPTION 'agent run effects may only advance from begun to committed';
    END IF;
    IF NEW.head_id <> OLD.head_id
       OR NEW.execution_attempt <> OLD.execution_attempt
       OR NEW.tool_call_id <> OLD.tool_call_id
       OR NEW.tool_name <> OLD.tool_name
       OR NEW.provider <> OLD.provider
       OR NEW.adapter_version <> OLD.adapter_version
       OR NEW.read_only <> OLD.read_only
       OR NEW.idempotency_key <> OLD.idempotency_key
       OR NEW.idempotency_contract <> OLD.idempotency_contract
       OR NEW.begun_at <> OLD.begun_at THEN
        RAISE EXCEPTION 'agent run effect identity is immutable';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER agent_run_effects_monotonic
BEFORE UPDATE OR DELETE ON agent_run_effects
FOR EACH ROW EXECUTE FUNCTION enforce_agent_run_effect_transition();

-- Event rows are immutable throughout the bounded retention window.
CREATE OR REPLACE FUNCTION enforce_agent_run_event_append_only() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' AND agent_run_checkpoint_head_cleanup_eligible(OLD.head_id) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'agent run events are append-only';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER agent_run_events_append_only
BEFORE UPDATE OR DELETE ON agent_run_events
FOR EACH ROW EXECUTE FUNCTION enforce_agent_run_event_append_only();

CREATE INDEX agent_run_checkpoints_expiration
ON agent_run_checkpoints (status, stage_expires_at, superseded_at, committed_at);

CREATE INDEX agent_checkpoint_objects_reconciliation
ON agent_checkpoint_objects
  (status, upload_expires_at, reconciliation_lease_expires_at)
WHERE status = 'uploading';

CREATE INDEX agent_checkpoint_objects_deletion
ON agent_checkpoint_objects (status, delete_lease_expires_at)
WHERE status = 'deleting';

CREATE INDEX agent_run_events_head_created
ON agent_run_events (head_id, created_at, id);

-- ---------- replayed from 1773106145_create_agent_run_attempts.up.sql ----------

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

-- A recovery source is pinned to the exact checkpoint generation under the
-- same durable head. The factory still requires committed status for recovery;
-- this key prevents a retained attempt from ever drifting cross-head or losing
-- its referenced row during expiry/cleanup.
CREATE UNIQUE INDEX agent_run_checkpoints_head_id_generation
    ON agent_run_checkpoints (head_id, id, generation);

ALTER TABLE agent_run_attempts
    ADD CONSTRAINT agent_run_attempts_source_checkpoint_same_head
    FOREIGN KEY (head_id, source_checkpoint_id, source_checkpoint_generation)
    REFERENCES agent_run_checkpoints (head_id, id, generation)
    ON DELETE RESTRICT;

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

-- ---------- replayed from 1773106146_attribute_metrics_to_agent_run_attempts.up.sql ----------

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

-- ---------- replayed from 1773106147_persist_agent_run_attempt_transcripts.up.sql ----------

-- Recovery creates a fresh durable attempt row even though it reuses the
-- build/plan identity. Transcript bytes must therefore identify the exact
-- server-created attempt instead of overwriting the legacy build/plan view.
--
-- Keep the denormalized identity as an audit/read model, but verify it against
-- agent_run_attempts and its immutable checkpoint head on every insert.
CREATE TABLE agent_run_attempt_transcripts (
    attempt_id BIGINT PRIMARY KEY
        REFERENCES agent_run_attempts (id) ON DELETE RESTRICT,
    build_id INTEGER NOT NULL,
    plan_id TEXT NOT NULL,
    execution_attempt INTEGER NOT NULL CHECK (execution_attempt > 0),
    workflow_run_id BIGINT,
    function_id TEXT NOT NULL DEFAULT '',
    step_name TEXT,
    ndjson TEXT NOT NULL,
    byte_len INTEGER NOT NULL CHECK (byte_len >= 0 AND byte_len = octet_length(ndjson)),
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    display_finalized BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX agent_run_attempt_transcripts_workflow_run
    ON agent_run_attempt_transcripts (workflow_run_id, created_at)
    WHERE workflow_run_id IS NOT NULL;

CREATE UNIQUE INDEX agent_run_attempt_transcripts_one_final_presentation
    ON agent_run_attempt_transcripts (build_id, plan_id)
    WHERE display_finalized;

CREATE FUNCTION enforce_agent_run_attempt_transcript_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM 1
        FROM agent_run_attempts AS a
        JOIN agent_run_checkpoint_heads AS h ON h.id = a.head_id
        WHERE a.id = NEW.attempt_id
          AND h.build_id = NEW.build_id
          AND h.plan_id = NEW.plan_id
          AND a.attempt_number = NEW.execution_attempt
          AND h.workflow_run_provenance_id IS NOT DISTINCT FROM NEW.workflow_run_id
          AND h.function_id = NEW.function_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'agent run attempt transcript identity does not match durable attempt';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.attempt_id <> OLD.attempt_id
       OR NEW.build_id <> OLD.build_id
       OR NEW.plan_id <> OLD.plan_id
       OR NEW.execution_attempt <> OLD.execution_attempt
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.function_id <> OLD.function_id
       OR NEW.step_name IS DISTINCT FROM OLD.step_name THEN
        RAISE EXCEPTION 'agent run attempt transcript identity is immutable';
    END IF;
    IF OLD.display_finalized AND NOT NEW.display_finalized THEN
        RAISE EXCEPTION 'an agent run attempt transcript final presentation is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_run_attempt_transcripts_immutable_identity
BEFORE INSERT OR UPDATE ON agent_run_attempt_transcripts
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_run_attempt_transcript_identity();
