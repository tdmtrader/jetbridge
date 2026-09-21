-- A selected output shares the producer's exact execution. Its verified
-- cancellation proof closes that execution without inventing another outcome.
-- Source release remains a separate requirement of run_output_closed.
DROP FUNCTION run_execution_closed(integer);
CREATE FUNCTION run_execution_closed(build bigint) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM pipeline_run_executions e
 LEFT JOIN pipeline_run_execution_closures c USING(execution_id,execution_fence)
 WHERE e.build_id=build AND c.execution_id IS NULL
 AND NOT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence x
  WHERE x.handoff_id=e.handoff_id
  AND (x.classification<>'never_started' OR NOT EXISTS(
   SELECT 1 FROM pipeline_run_execution_starts s WHERE s.execution_id=e.execution_id AND s.execution_fence=e.execution_fence))));
$$;
