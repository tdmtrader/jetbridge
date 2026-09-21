DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_run_execution_starts) OR EXISTS(SELECT 1 FROM pipeline_run_execution_closures) THEN
  RAISE EXCEPTION 'cannot discard retained Run execution witnesses';
 END IF;
END $$;
DROP FUNCTION run_execution_closed(integer);
DROP TABLE pipeline_run_execution_closures;
DROP TABLE pipeline_run_execution_starts;
DROP FUNCTION check_run_execution_evidence();
