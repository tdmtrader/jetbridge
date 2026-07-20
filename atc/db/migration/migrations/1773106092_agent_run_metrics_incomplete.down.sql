-- Non-lossy revert: incomplete rows are real measurements, so fold them to the
-- pre-L-1 vocabulary ('error') rather than deleting, then restore the constraint.
UPDATE agent_run_metrics SET status = 'error' WHERE status = 'incomplete';
ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked'));
