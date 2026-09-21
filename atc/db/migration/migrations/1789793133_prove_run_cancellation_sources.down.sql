DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence) THEN
  RAISE EXCEPTION 'cannot discard retained cancellation source evidence';
 END IF;
END $$;
DROP TRIGGER run_cancel_source_proof ON pipeline_run_output_finishes;
DROP FUNCTION check_run_cancel_source_proof();
DROP TABLE pipeline_run_output_cancellation_evidence;
