DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_classifications) THEN
  RAISE EXCEPTION 'cannot discard retained cancellation classification';
 END IF;
END $$;
DROP TRIGGER run_cancel_classified ON pipeline_run_output_cancellation_evidence;
DROP FUNCTION check_run_cancel_classified();
DROP TABLE pipeline_run_output_cancellation_classifications;
