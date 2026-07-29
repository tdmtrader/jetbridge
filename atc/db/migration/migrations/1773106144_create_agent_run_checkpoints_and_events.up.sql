-- Checkpoint recovery is intentionally separate from semantic snapshots. A
-- committed manifest is PostgreSQL's recovery authority; the immutable bytes
-- it names remain Hangar's authority.
CREATE TABLE agent_run_checkpoint_heads (
    id BIGSERIAL PRIMARY KEY,
    -- This is copied v3 workflow-run identity, not a lifecycle owner. A
    -- checkpoint must retain its attribution even after operational rows age
    -- out, and may never terminalize the workflow run.
    workflow_run_id BIGINT,
    build_id BIGINT NOT NULL,
    plan_id TEXT NOT NULL,
    function_id TEXT NOT NULL,
    latest_checkpoint_id BIGINT,
    next_generation INTEGER NOT NULL DEFAULT 1 CHECK (next_generation > 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    terminal_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (build_id, plan_id, function_id)
);

CREATE OR REPLACE FUNCTION enforce_agent_run_checkpoint_head_identity()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
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
