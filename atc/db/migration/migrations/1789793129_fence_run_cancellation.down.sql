DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_runs WHERE cancel_requested_at IS NOT NULL) THEN
  RAISE EXCEPTION 'cannot remove durable Run cancellation requests';
 END IF;
END $$;
ALTER TABLE pipeline_run_output_discards DROP CONSTRAINT pipeline_run_output_discards_reason_check,
 ADD CONSTRAINT pipeline_run_output_discards_reason_check CHECK(reason='build_aborted');
DROP TRIGGER run_cancellation_immutable ON pipeline_runs;
DROP FUNCTION immutable_run_cancellation();
ALTER TABLE pipeline_runs DROP CONSTRAINT run_cancel_request_shape,
 DROP COLUMN cancel_requested_at, DROP COLUMN cancel_requested_by, DROP COLUMN cancel_reason;
