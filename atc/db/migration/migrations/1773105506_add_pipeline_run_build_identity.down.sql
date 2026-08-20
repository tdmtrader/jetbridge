SELECT ensure_pipeline_template_runs_empty();

DROP TRIGGER build_pipeline_run_identity_immutable ON builds;
DROP FUNCTION immutable_build_pipeline_run_identity();
DROP INDEX builds_pipeline_run_job_key_id_idx;

ALTER TABLE builds
  DROP CONSTRAINT builds_pipeline_run_identity_complete,
  DROP COLUMN run_job_key,
  DROP COLUMN run_job_name,
  DROP COLUMN pipeline_run_id;
