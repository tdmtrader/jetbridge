-- Every run build carries a non-empty run job key.
--
-- The run job key is the one identity a job keeps across runs of a template:
-- the template's source job name. It is stamped on each build so history can
-- be read back after the run's payload -- the jobs rows included -- has been
-- reclaimed.
--
-- Materialization recorded the key only for jobs whose name interpolation
-- rewrote; an uninterpolated job's key was NULL in the map. CreateRun papered
-- over that by substituting the job name, and the schema let the empty string
-- through on both tables. That fallback landed with the columns, and a
-- payload cannot be re-saved, so no shipped database is expected to hold an
-- empty key -- this migration's backfill is a no-op on such a database. What
-- it changes is where the contract lives: materialization now keys every job,
-- the schema refuses the empty string, and the column on jobs takes the
-- domain's name for the concept (run_job_key) rather than "policy key".
--
-- The backfill is kept for any row written outside the factory's path. A job
-- with no key was never renamed, so its name is its source name; likewise a
-- build's run job name. The down migration restores the old shape and keeps
-- the backfilled values, which the old code tolerates.

-- The trigger's column list follows the rename by attribute number; only the
-- function body names the column and must be replaced.
ALTER TABLE jobs RENAME COLUMN run_policy_key TO run_job_key;

CREATE OR REPLACE FUNCTION check_run_job_metadata() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.run_expected OR NEW.run_job_key IS NOT NULL THEN
    IF NOT EXISTS (SELECT 1 FROM pipelines WHERE id = NEW.pipeline_id AND pipeline_run_id IS NOT NULL) THEN
      RAISE EXCEPTION 'run job metadata requires a payload pipeline';
    END IF;
  END IF;

  IF TG_OP = 'UPDATE' AND (NEW.run_expected IS DISTINCT FROM OLD.run_expected OR NEW.run_job_key IS DISTINCT FROM OLD.run_job_key) THEN
    RAISE EXCEPTION 'run job metadata is immutable';
  END IF;
  RETURN NEW;
END;
$$;

-- A job with no key was never renamed, so its name is its source name. The
-- metadata trigger is lifted for the backfill only: recording a key that was
-- never written is not a change of identity.
ALTER TABLE jobs DISABLE TRIGGER run_job_metadata_check;

UPDATE jobs j
SET run_job_key = j.name
FROM pipelines p
WHERE p.id = j.pipeline_id
  AND p.pipeline_run_id IS NOT NULL
  AND (j.run_job_key IS NULL OR j.run_job_key = '');

ALTER TABLE jobs ENABLE TRIGGER run_job_metadata_check;

ALTER TABLE jobs
  ADD CONSTRAINT jobs_run_job_key_nonempty CHECK (run_job_key IS NULL OR run_job_key <> '');

-- Likewise for builds: an empty key means the job was never renamed, so the
-- run job name is the source name.
ALTER TABLE builds DISABLE TRIGGER build_pipeline_run_identity_immutable;

UPDATE builds
SET run_job_key = run_job_name
WHERE pipeline_run_id IS NOT NULL AND run_job_key = '';

ALTER TABLE builds ENABLE TRIGGER build_pipeline_run_identity_immutable;

ALTER TABLE builds
  DROP CONSTRAINT builds_pipeline_run_identity_complete,
  ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
    (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL)
    OR (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL AND run_job_key <> '')
  );
