DROP FUNCTION run_execution_closed(bigint);
CREATE FUNCTION run_execution_closed(build integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM pipeline_run_executions e
 LEFT JOIN pipeline_run_execution_closures c USING(execution_id,execution_fence)
 WHERE e.build_id=build AND c.execution_id IS NULL);
$$;
