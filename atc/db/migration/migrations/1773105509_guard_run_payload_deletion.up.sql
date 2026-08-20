CREATE INDEX pipeline_runs_terminal_number_idx
  ON pipeline_runs (template_pipeline_id, number)
  WHERE status IN ('succeeded', 'failed', 'errored', 'aborted');

CREATE INDEX pipeline_runs_terminal_completed_at_idx
  ON pipeline_runs (template_pipeline_id, completed_at)
  WHERE status IN ('succeeded', 'failed', 'errored', 'aborted') AND completed_at IS NOT NULL;

CREATE OR REPLACE FUNCTION guard_run_payload_deletion() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
  owner_status text;
BEGIN
  IF OLD.pipeline_run_id IS NULL THEN
    RETURN OLD;
  END IF;

  IF current_setting('concourse.pipeline_run_team_purge', true) = 'on' THEN
    RETURN OLD;
  END IF;

  SELECT status INTO owner_status FROM pipeline_runs WHERE id = OLD.pipeline_run_id;
  IF owner_status IS NULL OR owner_status = 'running' OR EXISTS (
    SELECT 1
    FROM builds
    WHERE pipeline_run_id = OLD.pipeline_run_id
      AND run_job_name IS NOT NULL
      AND (job_id IS NOT NULL OR pipeline_id IS NOT NULL)
  ) THEN
    RAISE EXCEPTION 'run payload cannot be deleted before terminal retained builds are detached';
  END IF;

  RETURN OLD;
END;
$$;

CREATE TRIGGER run_payload_delete_guard
  BEFORE DELETE ON pipelines
  FOR EACH ROW EXECUTE FUNCTION guard_run_payload_deletion();
