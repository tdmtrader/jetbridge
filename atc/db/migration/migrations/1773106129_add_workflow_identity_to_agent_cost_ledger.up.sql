-- Server-owned identity for agent spend. The v2 plan-env attribution
-- (AGENT_TICKET_ID / AGENT_PIPELINE_RUN_ID) no longer exists: nothing emits
-- those vars and the v3 renderer rejects interpolation inside agent steps, so
-- ticket_id/pipeline_run_id could only ever be NULL. Spend is now keyed on the
-- durable workflow run and the workflow function that spent it — the same
-- identity agent_run_metrics took in migration 1773106124.
ALTER TABLE agent_cost_ledger
    ADD COLUMN workflow_run_id BIGINT,
    ADD COLUMN function_id TEXT NOT NULL DEFAULT '';

-- Exact backfill only, from the workflow run's PLANNED build. A build with no
-- planned workflow run (a pure-CI invocation, or a later anomaly build) keeps
-- workflow_run_id NULL — there is no guessing across unrelated builds.
UPDATE agent_cost_ledger ledger
SET workflow_run_id = run.id
FROM agent_workflow_runs run
WHERE ledger.build_id > 0 AND run.planned_build_id = ledger.build_id;

-- The run row is authoritative; deleting it releases the spend row's identity
-- (SET NULL) but never destroys the append-only ledger row.
ALTER TABLE agent_cost_ledger
    ADD CONSTRAINT agent_cost_ledger_workflow_run_fkey
    FOREIGN KEY (workflow_run_id)
    REFERENCES agent_workflow_runs (id)
    ON DELETE SET NULL;

CREATE INDEX agent_cost_ledger_workflow_run
    ON agent_cost_ledger (workflow_run_id, occurred_at DESC)
    WHERE workflow_run_id IS NOT NULL;

DROP INDEX agent_cost_ledger_ticket;
ALTER TABLE agent_cost_ledger
    DROP COLUMN ticket_id,
    DROP COLUMN pipeline_run_id;

-- gateway / harvest_judge / retrospective / probe named subsystems that were
-- removed with v2; nothing can write them again and their rows cannot satisfy
-- the narrowed CHECK. Relabelling would misattribute the spend, so the rows go.
DELETE FROM agent_cost_ledger WHERE source NOT IN ('agent_step', 'ci_agent');

ALTER TABLE agent_cost_ledger DROP CONSTRAINT agent_cost_ledger_source_check;
ALTER TABLE agent_cost_ledger
    ADD CONSTRAINT agent_cost_ledger_source_check
    CHECK (source IN ('agent_step', 'ci_agent'));
