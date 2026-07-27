-- Restore the pre-v3 SHAPE so an older binary can write these columns again.
-- The destroyed ticket/pipeline attribution and the deleted legacy-source rows
-- are gone: down does not invent identity or spend it cannot recover.
ALTER TABLE agent_cost_ledger DROP CONSTRAINT agent_cost_ledger_source_check;
ALTER TABLE agent_cost_ledger
    ADD CONSTRAINT agent_cost_ledger_source_check
    CHECK (source IN ('agent_step','gateway','harvest_judge','retrospective','ci_agent','probe'));

ALTER TABLE agent_cost_ledger
    ADD COLUMN ticket_id INTEGER,
    ADD COLUMN pipeline_run_id INTEGER;

CREATE INDEX agent_cost_ledger_ticket
    ON agent_cost_ledger (ticket_id) WHERE ticket_id IS NOT NULL;

DROP INDEX agent_cost_ledger_workflow_run;
ALTER TABLE agent_cost_ledger
    DROP CONSTRAINT agent_cost_ledger_workflow_run_fkey;
ALTER TABLE agent_cost_ledger
    DROP COLUMN workflow_run_id,
    DROP COLUMN function_id;
