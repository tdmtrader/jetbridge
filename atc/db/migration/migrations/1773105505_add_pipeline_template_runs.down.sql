SELECT ensure_pipeline_template_runs_empty();

DROP TRIGGER pipeline_run_child_required ON pipelines;
DROP TRIGGER pipeline_run_requires_child ON pipeline_runs;
DROP TRIGGER run_job_metadata_check ON jobs;
DROP TRIGGER pipeline_run_ownership_immutable ON pipelines;
DROP TRIGGER pipeline_run_immutable_fields ON pipeline_runs;
DROP TRIGGER pipeline_run_template_check ON pipeline_runs;

DROP FUNCTION require_running_pipeline_run_child();
DROP FUNCTION check_run_job_metadata();
DROP FUNCTION immutable_pipeline_run_ownership();
DROP FUNCTION immutable_pipeline_run_fields();
DROP FUNCTION check_pipeline_run_template();
DROP FUNCTION ensure_pipeline_template_runs_empty();

ALTER TABLE jobs
  DROP COLUMN run_policy_key,
  DROP COLUMN run_expected;

DROP INDEX pipeline_runs_reclaim_retry_after_idx;
DROP INDEX pipelines_pipeline_run_id_unique;

ALTER TABLE pipelines
  DROP CONSTRAINT pipelines_payloads_are_instances,
  DROP CONSTRAINT pipelines_templates_are_not_instances,
  DROP COLUMN pipeline_run_id;

DROP TABLE pipeline_runs;

ALTER TABLE pipelines
  DROP CONSTRAINT pipelines_last_run_number_non_negative,
  DROP CONSTRAINT pipelines_run_retention_ttl_days_positive,
  DROP CONSTRAINT pipelines_run_retention_keep_last_positive,
  DROP COLUMN last_run_number,
  DROP COLUMN run_retention_ttl_days,
  DROP COLUMN run_retention_keep_last,
  DROP COLUMN params,
  DROP COLUMN template;
