-- Reverse of the v3 reconciliation migration.
--
-- The status CHECK returns to the pre-'incomplete' vocabulary. Any row already
-- carrying status='incomplete' is normalized to 'error' first, because the
-- narrowed constraint would otherwise refuse to validate against existing data.
UPDATE agent_run_metrics SET status = 'error' WHERE status = 'incomplete';

ALTER TABLE agent_run_metrics DROP CONSTRAINT IF EXISTS agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked'));

DROP INDEX IF EXISTS agent_run_transcripts_workflow_run;
DROP TABLE IF EXISTS agent_run_transcripts;

DROP TABLE IF EXISTS agent_workflow_lifecycle;
