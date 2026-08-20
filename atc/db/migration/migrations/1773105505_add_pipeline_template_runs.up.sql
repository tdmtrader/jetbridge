ALTER TABLE pipelines
  ADD COLUMN template boolean NOT NULL DEFAULT false,
  ADD COLUMN params jsonb NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN run_retention_keep_last integer,
  ADD COLUMN run_retention_ttl_days integer,
  ADD COLUMN last_run_number integer NOT NULL DEFAULT 0,
  ADD CONSTRAINT pipelines_run_retention_keep_last_positive CHECK (run_retention_keep_last IS NULL OR run_retention_keep_last > 0),
  ADD CONSTRAINT pipelines_run_retention_ttl_days_positive CHECK (run_retention_ttl_days IS NULL OR run_retention_ttl_days > 0),
  ADD CONSTRAINT pipelines_last_run_number_non_negative CHECK (last_run_number >= 0);

CREATE TABLE pipeline_runs (
  id bigserial PRIMARY KEY,
  template_pipeline_id integer NOT NULL REFERENCES pipelines(id) ON DELETE RESTRICT,
  number integer NOT NULL CHECK (number > 0),
  params jsonb NOT NULL,
  status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'errored', 'aborted')),
  created_by text NOT NULL,
  created_at timestamp with time zone NOT NULL DEFAULT now(),
  completed_at timestamp with time zone,
  reclaim_retry_after timestamp with time zone,
  config_hash text NOT NULL,
  UNIQUE (template_pipeline_id, number)
);

ALTER TABLE pipelines
  ADD COLUMN pipeline_run_id bigint REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
  ADD CONSTRAINT pipelines_templates_are_not_instances CHECK (NOT template OR (instance_vars IS NULL AND pipeline_run_id IS NULL)),
  ADD CONSTRAINT pipelines_payloads_are_instances CHECK (pipeline_run_id IS NULL OR (NOT template AND instance_vars IS NOT NULL));

CREATE UNIQUE INDEX pipelines_pipeline_run_id_unique ON pipelines (pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;
CREATE INDEX pipeline_runs_reclaim_retry_after_idx ON pipeline_runs (reclaim_retry_after) WHERE reclaim_retry_after IS NOT NULL;

ALTER TABLE jobs
  ADD COLUMN run_expected boolean NOT NULL DEFAULT false,
  ADD COLUMN run_policy_key text;

CREATE OR REPLACE FUNCTION check_pipeline_run_template() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pipelines WHERE id = NEW.template_pipeline_id AND template) THEN
    RAISE EXCEPTION 'pipeline run template pipeline must be a template';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER pipeline_run_template_check
  BEFORE INSERT OR UPDATE OF template_pipeline_id ON pipeline_runs
  FOR EACH ROW EXECUTE FUNCTION check_pipeline_run_template();

CREATE OR REPLACE FUNCTION prevent_referenced_pipeline_from_becoming_non_template() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.template AND NOT NEW.template
    AND EXISTS (SELECT 1 FROM pipeline_runs WHERE template_pipeline_id = OLD.id) THEN
    RAISE EXCEPTION 'template pipeline referenced by pipeline runs cannot become non-template';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER referenced_pipeline_template_check
  BEFORE UPDATE OF template ON pipelines
  FOR EACH ROW EXECUTE FUNCTION prevent_referenced_pipeline_from_becoming_non_template();

CREATE OR REPLACE FUNCTION immutable_pipeline_run_fields() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.template_pipeline_id IS DISTINCT FROM OLD.template_pipeline_id
    OR NEW.number IS DISTINCT FROM OLD.number
    OR NEW.params IS DISTINCT FROM OLD.params
    OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.created_at IS DISTINCT FROM OLD.created_at
    OR NEW.config_hash IS DISTINCT FROM OLD.config_hash THEN
    RAISE EXCEPTION 'pipeline run ownership and configuration are immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER pipeline_run_immutable_fields
  BEFORE UPDATE ON pipeline_runs
  FOR EACH ROW EXECUTE FUNCTION immutable_pipeline_run_fields();

CREATE OR REPLACE FUNCTION immutable_pipeline_run_ownership() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.pipeline_run_id IS DISTINCT FROM OLD.pipeline_run_id THEN
    RAISE EXCEPTION 'pipeline run ownership is immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER pipeline_run_ownership_immutable
  BEFORE UPDATE OF pipeline_run_id ON pipelines
  FOR EACH ROW EXECUTE FUNCTION immutable_pipeline_run_ownership();

CREATE OR REPLACE FUNCTION check_run_job_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.run_expected OR NEW.run_policy_key IS NOT NULL THEN
    IF NOT EXISTS (SELECT 1 FROM pipelines WHERE id = NEW.pipeline_id AND pipeline_run_id IS NOT NULL) THEN
      RAISE EXCEPTION 'run job metadata requires a payload pipeline';
    END IF;
  END IF;

  IF TG_OP = 'UPDATE' AND (NEW.run_expected IS DISTINCT FROM OLD.run_expected OR NEW.run_policy_key IS DISTINCT FROM OLD.run_policy_key) THEN
    RAISE EXCEPTION 'run job metadata is immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER run_job_metadata_check
  BEFORE INSERT OR UPDATE OF run_expected, run_policy_key ON jobs
  FOR EACH ROW EXECUTE FUNCTION check_run_job_metadata();

CREATE OR REPLACE FUNCTION require_running_pipeline_run_child() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  checked_run_id bigint;
  child_count integer;
  run_status text;
BEGIN
  IF TG_TABLE_NAME = 'pipeline_runs' THEN
    checked_run_id := NEW.id;
  ELSIF TG_OP = 'DELETE' THEN
    checked_run_id := OLD.pipeline_run_id;
  ELSE
    checked_run_id := NEW.pipeline_run_id;
  END IF;

  IF checked_run_id IS NULL THEN
    RETURN NULL;
  END IF;

  SELECT status INTO run_status FROM pipeline_runs WHERE id = checked_run_id;
  IF run_status = 'running' THEN
    SELECT count(*) INTO child_count FROM pipelines WHERE pipeline_run_id = checked_run_id;
    IF child_count <> 1 THEN
      RAISE EXCEPTION 'running pipeline run requires exactly one payload pipeline';
    END IF;
  END IF;

  RETURN NULL;
END;
$$;

CREATE CONSTRAINT TRIGGER pipeline_run_requires_child
  AFTER INSERT OR UPDATE OF status ON pipeline_runs
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION require_running_pipeline_run_child();

CREATE CONSTRAINT TRIGGER pipeline_run_child_required
  AFTER INSERT OR UPDATE OF pipeline_run_id OR DELETE ON pipelines
  DEFERRABLE INITIALLY DEFERRED
  FOR EACH ROW EXECUTE FUNCTION require_running_pipeline_run_child();

CREATE OR REPLACE FUNCTION ensure_pipeline_template_runs_empty() RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  feature_exists boolean := false;
  has_build_run_identity boolean;
  has_run_cache_template_pipeline_id boolean;
  has_run_cache_job_name boolean;
  feature_rows boolean;
BEGIN
  SELECT EXISTS (SELECT 1 FROM pipelines WHERE template OR pipeline_run_id IS NOT NULL)
      OR EXISTS (SELECT 1 FROM pipeline_runs)
    INTO feature_exists;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'builds' AND column_name = 'pipeline_run_id'
  ) INTO has_build_run_identity;
  IF has_build_run_identity THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM builds WHERE pipeline_run_id IS NOT NULL)' INTO feature_rows;
    IF feature_rows THEN
      RAISE EXCEPTION 'cannot roll back pipeline template runs while templates, runs, run builds, or run caches remain';
    END IF;
  END IF;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'task_caches' AND column_name = 'template_pipeline_id'
  ) INTO has_run_cache_template_pipeline_id;
  IF has_run_cache_template_pipeline_id THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM task_caches WHERE template_pipeline_id IS NOT NULL)' INTO feature_rows;
    IF feature_rows THEN
      RAISE EXCEPTION 'cannot roll back pipeline template runs while templates, runs, run builds, or run caches remain';
    END IF;
  END IF;

  SELECT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'task_caches' AND column_name = 'run_job_name'
  ) INTO has_run_cache_job_name;
  IF has_run_cache_job_name THEN
    EXECUTE 'SELECT EXISTS (SELECT 1 FROM task_caches WHERE run_job_name IS NOT NULL)' INTO feature_rows;
    IF feature_rows THEN
      RAISE EXCEPTION 'cannot roll back pipeline template runs while templates, runs, run builds, or run caches remain';
    END IF;
  END IF;

  IF feature_exists THEN
    RAISE EXCEPTION 'cannot roll back pipeline template runs while templates, runs, run builds, or run caches remain';
  END IF;
END;
$$;
