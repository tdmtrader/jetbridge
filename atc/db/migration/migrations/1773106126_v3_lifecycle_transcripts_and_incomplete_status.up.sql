-- v3 reconciliation migration.
--
-- The v3-only cutover removed main's 1773106092-095 (they were authored against
-- the ticket-centric schema and, on a database already past 1773106100, would
-- never apply). Three of the things they provided are still wanted, so v3 owns
-- them here on its own lineage:
--
--   1. agent_workflow_lifecycle  (was 1773106094) — carried verbatim.
--   2. agent_run_transcripts     (was 1773106093) — RE-KEYED onto durable
--      workflow-run identity: ticket_id is gone (a ticket is a work-item
--      adapter, never an execution identity); workflow_run_id + function_id
--      are the v3 execution identity, matching agent_run_metrics.
--   3. the agent_run_metrics status CHECK relaxation (was 1773106092) — the
--      L-1 'incomplete' tier, which atc/exec/agent_step.go now writes when a
--      flight recording is missing. Without this every degraded ingestion
--      would fail its INSERT instead of landing an amber row.
--
-- Statements are written defensively so this applies both to a fresh database
-- and to one that already ran main's 1773106092-095 before the cutover.

-- 1. Workflow-name-level lifecycle metadata. Distinct from
-- agent_workflow_definitions (per-version): annotation is a human operator
-- note, hidden deprecates a workflow from default listings without deleting
-- its versions. Keyed by name; the row is created lazily on first annotate/hide.
CREATE TABLE IF NOT EXISTS agent_workflow_lifecycle (
    name       TEXT PRIMARY KEY,
    hidden     BOOLEAN NOT NULL DEFAULT false,
    annotation TEXT NOT NULL DEFAULT '',
    updated_by TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 2. Agent transcript observability: the agent's turn-by-turn tool-call
-- transcript (raw `claude --output-format stream-json --verbose` stdout, one
-- JSON object per line). The runner writes flight/transcript.ndjson and the
-- agent step ingests it here, keyed like agent_run_metrics on (build_id, plan_id).
-- Any pre-cutover ticket-keyed table is dropped: its rows carry an identity v3
-- does not recognize and cannot be migrated to a workflow run.
DROP TABLE IF EXISTS agent_run_transcripts;

CREATE TABLE agent_run_transcripts (
    build_id        INTEGER     NOT NULL,
    plan_id         TEXT        NOT NULL,
    workflow_run_id BIGINT      REFERENCES agent_workflow_runs (id) ON DELETE SET NULL,
    function_id     TEXT        NOT NULL DEFAULT '',
    step_name       TEXT,
    ndjson          TEXT        NOT NULL,
    byte_len        INTEGER     NOT NULL,
    truncated       BOOLEAN     NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (build_id, plan_id)
);

CREATE INDEX agent_run_transcripts_workflow_run
    ON agent_run_transcripts (workflow_run_id, created_at)
    WHERE workflow_run_id IS NOT NULL;

-- 3. L-1 'incomplete': a flight ingestion that reads NO flight output records
-- status = 'incomplete' — a missing RECORDING (dominant cause: a runner image
-- predating the flight recorder), not a failed step. DeriveOutcome fuses it to
-- the amber 'unrecorded' outcome on a succeeded build (never red).
ALTER TABLE agent_run_metrics DROP CONSTRAINT IF EXISTS agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked','incomplete'));
