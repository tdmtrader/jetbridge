-- Restores the column SHAPE (so an older binary can write the tags again) and
-- the lookup index. The dropped tag values are gone.
ALTER TABLE agent_run_metrics
    ADD COLUMN workflow_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN workflow_version INTEGER,
    ADD COLUMN workflow_hash TEXT NOT NULL DEFAULT '';

CREATE INDEX agent_run_metrics_workflow ON agent_run_metrics (workflow_name, workflow_version);
