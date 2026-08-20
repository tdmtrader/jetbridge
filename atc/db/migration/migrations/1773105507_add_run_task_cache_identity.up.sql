DROP INDEX task_caches_job_id_step_name_path_uniq;

ALTER TABLE task_caches
  ALTER COLUMN job_id DROP NOT NULL,
  ADD COLUMN template_pipeline_id integer REFERENCES pipelines(id) ON DELETE CASCADE,
  ADD COLUMN run_job_name text,
  ADD CONSTRAINT task_caches_identity_complete CHECK (
    (job_id IS NOT NULL AND template_pipeline_id IS NULL AND run_job_name IS NULL)
    OR
    (job_id IS NULL AND template_pipeline_id IS NOT NULL AND run_job_name IS NOT NULL)
  );

CREATE UNIQUE INDEX task_caches_job_id_step_name_path_uniq
  ON task_caches (job_id, step_name, path)
  WHERE job_id IS NOT NULL;

CREATE UNIQUE INDEX task_caches_run_identity_unique
  ON task_caches (template_pipeline_id, run_job_name, step_name, path)
  WHERE template_pipeline_id IS NOT NULL;

CREATE INDEX task_caches_template_pipeline_id_idx
  ON task_caches (template_pipeline_id)
  WHERE template_pipeline_id IS NOT NULL;
