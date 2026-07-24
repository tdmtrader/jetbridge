-- Recreate the nullable legacy ticket/pipeline identity columns and the ticket
-- index, then drop the durable workflow-run identity. The destroyed ticket
-- values are gone: down restores the SHAPE (so an older binary can write the
-- columns again) but does not invent ticket identity it cannot recover.
ALTER TABLE agent_run_metrics
    ADD COLUMN ticket_id INTEGER,
    ADD COLUMN pipeline_run_id INTEGER;

CREATE INDEX agent_run_metrics_ticket
    ON agent_run_metrics (ticket_id) WHERE ticket_id IS NOT NULL;

DROP INDEX agent_run_metrics_workflow_run;
ALTER TABLE agent_run_metrics
    DROP CONSTRAINT agent_run_metrics_workflow_run_fkey;
ALTER TABLE agent_run_metrics
    DROP COLUMN workflow_run_id,
    DROP COLUMN function_id;
