SELECT ensure_pipeline_template_runs_empty();

DROP INDEX task_caches_template_pipeline_id_idx;
DROP INDEX task_caches_run_identity_unique;
DROP INDEX task_caches_job_id_step_name_path_uniq;

ALTER TABLE task_caches
  DROP CONSTRAINT task_caches_identity_complete,
  DROP COLUMN run_job_name,
  DROP COLUMN template_pipeline_id,
  ALTER COLUMN job_id SET NOT NULL;

CREATE UNIQUE INDEX task_caches_job_id_step_name_path_uniq
  ON task_caches (job_id, step_name, path);
