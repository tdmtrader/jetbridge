ALTER TABLE builds
  ADD COLUMN pipeline_run_id bigint REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
  ADD COLUMN run_job_name text,
  ADD COLUMN run_job_key text,
  ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
    (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL)
    OR (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL)
  );

CREATE INDEX builds_pipeline_run_job_key_id_idx ON builds (pipeline_run_id, run_job_key, id DESC) WHERE pipeline_run_id IS NOT NULL;

CREATE OR REPLACE FUNCTION immutable_build_pipeline_run_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.pipeline_run_id IS DISTINCT FROM OLD.pipeline_run_id
    OR NEW.run_job_name IS DISTINCT FROM OLD.run_job_name
    OR NEW.run_job_key IS DISTINCT FROM OLD.run_job_key THEN
    RAISE EXCEPTION 'build pipeline run identity is immutable';
  END IF;
  RETURN NEW;
END;
$$;

CREATE TRIGGER build_pipeline_run_identity_immutable
  BEFORE UPDATE OF pipeline_run_id, run_job_name, run_job_key ON builds
  FOR EACH ROW EXECUTE FUNCTION immutable_build_pipeline_run_identity();
