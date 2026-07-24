-- Durable workflow-run identity for agent run metrics (outcome-metrics
-- consolidation, Task 1). The active execution identity is the schema-v3
-- workflow run, not the optional work-item ticket. A metric is bound to the
-- workflow run whose PLANNED build produced it (exact build identity, never
-- inferred by type/order/timestamp), and to the workflow function that ran.
-- The (build_id, plan_id) ingestion idempotency key is unchanged.
ALTER TABLE agent_run_metrics
    ADD COLUMN workflow_run_id BIGINT,
    ADD COLUMN function_id TEXT NOT NULL DEFAULT '';

-- Backfill from the workflow run's planned build only. A build with no
-- planned workflow run (a pure-CI invocation) keeps workflow_run_id NULL —
-- there is no guessing across unrelated builds.
UPDATE agent_run_metrics metric
SET workflow_run_id = run.id
FROM agent_workflow_runs run
WHERE run.planned_build_id = metric.build_id;

-- The run row is authoritative; deleting it releases the metric's identity
-- (SET NULL) but never destroys the durable telemetry row.
ALTER TABLE agent_run_metrics
    ADD CONSTRAINT agent_run_metrics_workflow_run_fkey
    FOREIGN KEY (workflow_run_id)
    REFERENCES agent_workflow_runs (id)
    ON DELETE SET NULL;

CREATE INDEX agent_run_metrics_workflow_run
    ON agent_run_metrics (workflow_run_id, created_at, id)
    WHERE workflow_run_id IS NOT NULL;

DROP INDEX agent_run_metrics_ticket;
ALTER TABLE agent_run_metrics
    DROP COLUMN ticket_id,
    DROP COLUMN pipeline_run_id;
