-- A check's Run/build identity and execution evidence outlive its payload.
-- Resource links are disposable, just like a job build's job/pipeline links.
-- Only completed terminal checks may retain their identity without those links.
ALTER TABLE builds DROP CONSTRAINT builds_pipeline_run_identity_complete,
 ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
  (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NULL AND run_job_key IS NULL AND job_id IS NULL
   AND (resource_id IS NOT NULL OR resource_type_id IS NOT NULL OR
    (pipeline_id IS NULL AND completed AND status IN ('succeeded', 'failed', 'errored', 'aborted')))));

-- Detached checks arise from reclamation, never from new check admission.
CREATE OR REPLACE FUNCTION guard_run_check_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner pipeline_runs%ROWTYPE;
BEGIN
 IF NEW.pipeline_run_id IS NULL OR NEW.run_job_name IS NOT NULL THEN RETURN NEW; END IF;
 IF NEW.resource_id IS NULL AND NEW.resource_type_id IS NULL THEN
  RAISE EXCEPTION 'Run check admission requires a resource or resource type';
 END IF;
 SELECT * INTO owner FROM pipeline_runs WHERE id=NEW.pipeline_run_id FOR NO KEY UPDATE;
 IF owner.run_contract_version<>'v2' OR owner.status<>'running' OR owner.cancel_requested_at IS NOT NULL OR
    NOT EXISTS(SELECT 1 FROM pipelines WHERE id=NEW.pipeline_id AND pipeline_run_id=owner.id) THEN
  RAISE EXCEPTION 'Run check is not admitted';
 END IF;
 RETURN NEW;
END;
$$;
