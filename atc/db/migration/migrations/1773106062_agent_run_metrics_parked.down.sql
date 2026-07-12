ALTER TABLE agent_run_metrics DROP COLUMN session_id;
DELETE FROM agent_run_metrics WHERE status = 'parked';
ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error'));
