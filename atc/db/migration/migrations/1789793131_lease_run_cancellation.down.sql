DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_cancellation_worker) THEN
  RAISE EXCEPTION 'cannot reset a durable cancellation worker epoch';
 END IF;
END $$;
DROP TABLE pipeline_run_cancellation_worker;
