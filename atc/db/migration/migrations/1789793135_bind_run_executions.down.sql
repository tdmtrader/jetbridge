DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_executions) THEN
  RAISE EXCEPTION 'cannot discard retained Run execution identities';
 END IF;
END $$;
DROP TABLE pipeline_run_executions;
