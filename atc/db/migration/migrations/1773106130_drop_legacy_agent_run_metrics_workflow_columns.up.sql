-- The workflow tags on agent_run_metrics were written from the v2 plan-env
-- vars AGENT_WORKFLOW_NAME/VERSION/HASH, which no producer emits any more (the
-- v3 renderer rejects interpolation inside agent steps), so every v3 row
-- carries empty tags. The durable workflow run added in migration 1773106124
-- is the identity; workflow name/version/hash are read through
-- agent_workflow_runs.
DROP INDEX agent_run_metrics_workflow;
ALTER TABLE agent_run_metrics
    DROP COLUMN workflow_name,
    DROP COLUMN workflow_version,
    DROP COLUMN workflow_hash;
