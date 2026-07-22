ALTER TABLE agent_workflow_runs
    ADD COLUMN execution_status TEXT,
    ADD COLUMN reconcile_after TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE agent_workflow_runs
SET resolved_dependencies = '{
    "version": 1,
    "resources": [],
    "images": [],
    "platform_resource_types": []
}'
WHERE actual_plan IS NOT NULL
  AND actual_plan_hash IS NOT NULL
  AND resolved_dependencies IS NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_workflow_runs
        WHERE NOT (
            (actual_plan IS NULL
                AND actual_plan_hash IS NULL
                AND resolved_dependencies IS NULL)
            OR
            (actual_plan IS NOT NULL
                AND actual_plan_hash IS NOT NULL
                AND resolved_dependencies IS NOT NULL
                AND planned_build_id IS NOT NULL)
        )
    ) THEN
        RAISE EXCEPTION 'partial workflow-run plan provenance cannot be migrated';
    END IF;
END
$$;

ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT agent_workflow_runs_pipeline_run_id_fkey,
    DROP CONSTRAINT agent_workflow_runs_template_pipeline_id_fkey,
    DROP CONSTRAINT agent_workflow_runs_instance_pipeline_id_fkey,
    DROP CONSTRAINT agent_workflow_runs_planned_build_id_fkey,
    ADD CONSTRAINT agent_workflow_runs_execution_status_check
        CHECK (execution_status IS NULL OR execution_status IN ('succeeded', 'failed', 'errored', 'aborted')),
    ADD CONSTRAINT agent_workflow_runs_execution_status_build_check
        CHECK (execution_status IS NULL OR planned_build_id IS NOT NULL),
    ADD CONSTRAINT agent_workflow_runs_execution_link_complete_check
        CHECK (
            (pipeline_run_id IS NULL
                AND template_pipeline_id IS NULL
                AND instance_pipeline_id IS NULL
                AND concrete_config IS NULL
                AND concrete_config_hash IS NULL)
            OR
            (pipeline_run_id IS NOT NULL
                AND template_pipeline_id IS NOT NULL
                AND instance_pipeline_id IS NOT NULL
                AND concrete_config IS NOT NULL
                AND concrete_config_hash IS NOT NULL)
        ),
    ADD CONSTRAINT agent_workflow_runs_plan_complete_check
        CHECK (
            (actual_plan IS NULL
                AND actual_plan_hash IS NULL
                AND resolved_dependencies IS NULL)
            OR
            (actual_plan IS NOT NULL
                AND actual_plan_hash IS NOT NULL
                AND resolved_dependencies IS NOT NULL
                AND planned_build_id IS NOT NULL)
        );

CREATE UNIQUE INDEX agent_workflow_runs_pipeline_run_unique
    ON agent_workflow_runs (pipeline_run_id)
    WHERE pipeline_run_id IS NOT NULL;

CREATE UNIQUE INDEX agent_workflow_runs_instance_pipeline_unique
    ON agent_workflow_runs (instance_pipeline_id)
    WHERE instance_pipeline_id IS NOT NULL;

CREATE INDEX agent_workflow_runs_reconcile_due
    ON agent_workflow_runs (reconcile_after, id)
    WHERE status IN ('admitting', 'running', 'canceling');

CREATE TABLE agent_workflow_run_anomalies (
    id              BIGSERIAL PRIMARY KEY,
    workflow_run_id BIGINT NOT NULL REFERENCES agent_workflow_runs (id) ON DELETE CASCADE,
    kind            TEXT NOT NULL
                    CHECK (kind IN ('later_build_started', 'later_build_completed')),
    build_id        BIGINT NOT NULL CHECK (build_id > 0),
    build_status    TEXT NOT NULL
                    CHECK (build_status IN ('pending', 'started', 'succeeded', 'failed', 'errored', 'aborted')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workflow_run_id, kind, build_id)
);
