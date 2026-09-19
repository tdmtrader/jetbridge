ALTER TABLE builds
  DROP CONSTRAINT builds_pipeline_run_identity_complete,
  ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
    (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL)
    OR (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL)
  );

-- Backfilled keys are kept; the old code tolerates them.
ALTER TABLE jobs
  DROP CONSTRAINT jobs_run_job_key_nonempty;
ALTER TABLE jobs RENAME COLUMN run_job_key TO run_policy_key;

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
