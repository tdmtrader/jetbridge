-- One row per attempt of one semantic node within one workflow run. This is a
-- frozen projection of authoritative run/attempt/wait/publication state, not a
-- second execution truth: it is written once at run finalization, before
-- Concourse build and template GC can reclaim the records it was derived from.
-- Deterministic task steps exist only here after that point.
--
-- There are two independent attempt axes and both are part of the key.
-- retry_attempt is which copy of an authored retry closure the row belongs to:
-- atc/builds/planner.go materializes one full copy of the wrapped step per
-- configured attempt, each with its own plan ID, and atc/engine/builder.go
-- numbers those copies by index. attempt is which recovery attempt of that
-- copy it is, read from agent_run_attempt_metrics.execution_attempt. A key
-- carrying only one of them would collide the moment a retried step recorded
-- evidence for two copies.
CREATE TABLE agent_workflow_run_node_occurrences (
    workflow_run_id        BIGINT NOT NULL
        REFERENCES agent_workflow_runs (id) ON DELETE CASCADE,
    node_id                TEXT NOT NULL CHECK (btrim(node_id) <> ''),
    retry_attempt          INTEGER NOT NULL DEFAULT 1 CHECK (retry_attempt > 0),
    attempt                INTEGER NOT NULL DEFAULT 1 CHECK (attempt > 0),

    team_id                INTEGER NOT NULL CHECK (team_id > 0),
    workflow_name          TEXT NOT NULL CHECK (btrim(workflow_name) <> ''),
    workflow_definition_id INTEGER NOT NULL,
    workflow_version       INTEGER NOT NULL CHECK (workflow_version > 0),

    node_kind              TEXT NOT NULL
        CHECK (node_kind IN ('agent', 'task', 'await', 'publish', 'load')),
    reusable_node_name     TEXT NOT NULL DEFAULT '',
    reusable_node_version  INTEGER,
    plan_id                TEXT NOT NULL DEFAULT '',

    status                 TEXT NOT NULL
        CHECK (status IN ('pending', 'running', 'waiting', 'succeeded',
                          'failed', 'errored', 'aborted', 'skipped')),

    wait_id                BIGINT REFERENCES agent_workflow_waits (id) ON DELETE SET NULL,
    publication_id         BIGINT,

    started_at             TIMESTAMPTZ,
    completed_at           TIMESTAMPTZ,
    duration_seconds       INTEGER NOT NULL DEFAULT 0 CHECK (duration_seconds >= 0),
    cost_usd               NUMERIC(12,6) NOT NULL DEFAULT 0 CHECK (cost_usd >= 0),

    frozen_at              TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),

    PRIMARY KEY (workflow_run_id, node_id, retry_attempt, attempt),
    CHECK (completed_at IS NULL OR started_at IS NULL OR completed_at >= started_at),
    CHECK (reusable_node_version IS NULL OR reusable_node_version > 0),
    CHECK ((reusable_node_name = '') = (reusable_node_version IS NULL))
);

-- The cross-revision aggregate path: history for one workflow-local logical
-- role across every revision, bounded by the selected window.
CREATE INDEX agent_workflow_run_node_occurrences_history
    ON agent_workflow_run_node_occurrences
       (team_id, workflow_name, node_id, completed_at DESC);

-- The run page path.
CREATE INDEX agent_workflow_run_node_occurrences_run
    ON agent_workflow_run_node_occurrences (workflow_run_id, node_id);

-- A frozen occurrence is immutable history. Correcting one means the
-- derivation was wrong, which is a code fix plus a deliberate re-freeze, never
-- an in-place UPDATE.
--
-- The one exception is wait_id's own ON DELETE SET NULL. PostgreSQL implements
-- that referential action as an internal UPDATE, and a BEFORE ROW UPDATE
-- trigger fires for it, so an unconditional refusal makes the declared action
-- unexecutable: deleting a referenced agent_workflow_waits row aborts with
-- "agent workflow run node occurrences are immutable once frozen". Permit
-- exactly that clearing — the live reference goes, every durable fact stays —
-- and nothing else. This is the same carve-out
-- enforce_agent_workflow_run_ticket_immutability makes for the same reason.
CREATE FUNCTION enforce_agent_workflow_run_node_occurrence_immutability()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.wait_id IS NOT NULL AND NEW.wait_id IS NULL
       AND (to_jsonb(NEW) - 'wait_id') = (to_jsonb(OLD) - 'wait_id') THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'agent workflow run node occurrences are immutable once frozen';
END;
$$;

CREATE TRIGGER agent_workflow_run_node_occurrences_immutable
BEFORE UPDATE ON agent_workflow_run_node_occurrences
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_workflow_run_node_occurrence_immutability();

-- agent_publication_occurrences is keyed (publication_id, workflow_run_id,
-- build_id) and carries no plan identity, so a publish node cannot be joined
-- exactly to its occurrence. Add the plan ID going forward. Existing rows stay
-- NULL and fall back to build step state, which is correct because those runs
-- predate the projection entirely.
ALTER TABLE agent_publication_occurrences
    ADD COLUMN plan_id TEXT;

CREATE INDEX agent_publication_occurrences_plan
    ON agent_publication_occurrences (workflow_run_id, plan_id)
    WHERE plan_id IS NOT NULL;

-- The overview's dominant access path: one workflow's history window, newest
-- first, with active runs unioned in. NULLS FIRST matches the union: an active
-- run has no completed_at and must stay visible regardless of the window.
CREATE INDEX agent_workflow_runs_team_workflow_completed
    ON agent_workflow_runs (team_id, workflow_name, completed_at DESC NULLS FIRST, id DESC);
