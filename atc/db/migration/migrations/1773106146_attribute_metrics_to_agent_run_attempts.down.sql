DROP TRIGGER IF EXISTS agent_run_attempt_metrics_bound_identity ON agent_run_attempt_metrics;
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_metric_binding();
DROP TABLE IF EXISTS agent_run_attempt_metrics;
