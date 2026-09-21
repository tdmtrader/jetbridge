DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_cancellation_progress) OR EXISTS(SELECT 1 FROM pipeline_run_cancellation_operations) THEN
  RAISE EXCEPTION 'cannot discard durable Run cancellation progress';
 END IF;
END $$;
DROP TABLE pipeline_run_cancellation_cursors;
DROP TABLE pipeline_run_cancellation_operations;
DROP FUNCTION immutable_run_cancellation_operation();
DROP TABLE pipeline_run_cancellation_progress;
ALTER TABLE pipeline_run_cancellation_worker DROP COLUMN last_run_id, DROP COLUMN run_high_water;
