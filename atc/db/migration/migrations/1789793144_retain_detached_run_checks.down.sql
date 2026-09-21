DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id IS NOT NULL AND run_job_name IS NULL
           AND resource_id IS NULL AND resource_type_id IS NULL) THEN
  RAISE EXCEPTION 'cannot discard retained detached Run checks';
 END IF;
END $$;

ALTER TABLE builds DROP CONSTRAINT builds_pipeline_run_identity_complete,
 ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
  (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NULL AND run_job_key IS NULL AND job_id IS NULL
   AND (resource_id IS NOT NULL OR resource_type_id IS NOT NULL)));

CREATE OR REPLACE FUNCTION guard_run_check_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner pipeline_runs%ROWTYPE;
BEGIN
 IF NEW.pipeline_run_id IS NULL OR (NEW.resource_id IS NULL AND NEW.resource_type_id IS NULL) THEN RETURN NEW; END IF;
 SELECT * INTO owner FROM pipeline_runs WHERE id=NEW.pipeline_run_id FOR NO KEY UPDATE;
 IF owner.run_contract_version<>'v2' OR owner.status<>'running' OR owner.cancel_requested_at IS NOT NULL OR
    NOT EXISTS(SELECT 1 FROM pipelines WHERE id=NEW.pipeline_id AND pipeline_run_id=owner.id) THEN
  RAISE EXCEPTION 'Run check is not admitted';
 END IF;
 RETURN NEW;
END;
$$;
