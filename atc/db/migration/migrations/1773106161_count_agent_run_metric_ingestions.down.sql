-- The column only ever counted; nothing else depends on it.
ALTER TABLE agent_run_metrics DROP COLUMN ingestion_seq;
