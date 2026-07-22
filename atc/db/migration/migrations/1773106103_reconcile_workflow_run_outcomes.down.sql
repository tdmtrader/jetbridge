DROP TABLE agent_workflow_run_anomalies;

DROP INDEX agent_workflow_runs_reconcile_due;
DROP INDEX agent_workflow_runs_instance_pipeline_unique;
DROP INDEX agent_workflow_runs_pipeline_run_unique;

ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT agent_workflow_runs_plan_complete_check,
    DROP CONSTRAINT agent_workflow_runs_execution_link_complete_check,
    DROP CONSTRAINT agent_workflow_runs_execution_status_build_check,
    DROP CONSTRAINT agent_workflow_runs_execution_status_check;

UPDATE agent_workflow_runs AS run
SET pipeline_run_id = NULL,
    template_pipeline_id = NULL,
    instance_pipeline_id = NULL,
    concrete_config = NULL,
    concrete_config_hash = NULL
WHERE run.pipeline_run_id IS NOT NULL
  AND (
      NOT EXISTS (SELECT 1 FROM pipeline_runs WHERE id = run.pipeline_run_id)
      OR NOT EXISTS (SELECT 1 FROM pipelines WHERE id = run.template_pipeline_id)
      OR NOT EXISTS (SELECT 1 FROM pipelines WHERE id = run.instance_pipeline_id)
  );

UPDATE agent_workflow_runs AS run
SET planned_build_id = NULL
WHERE run.planned_build_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM builds WHERE id = run.planned_build_id);

ALTER TABLE agent_workflow_runs
    ADD CONSTRAINT agent_workflow_runs_pipeline_run_id_fkey
        FOREIGN KEY (pipeline_run_id) REFERENCES pipeline_runs (id) ON DELETE SET NULL,
    ADD CONSTRAINT agent_workflow_runs_template_pipeline_id_fkey
        FOREIGN KEY (template_pipeline_id) REFERENCES pipelines (id) ON DELETE SET NULL,
    ADD CONSTRAINT agent_workflow_runs_instance_pipeline_id_fkey
        FOREIGN KEY (instance_pipeline_id) REFERENCES pipelines (id) ON DELETE SET NULL,
    ADD CONSTRAINT agent_workflow_runs_planned_build_id_fkey
        FOREIGN KEY (planned_build_id) REFERENCES builds (id) ON DELETE SET NULL,
    DROP COLUMN execution_status,
    DROP COLUMN reconcile_after;
