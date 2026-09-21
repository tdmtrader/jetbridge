DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_invocations) THEN
        RAISE EXCEPTION 'cannot discard retained Run invocations';
    END IF;
END; $$;
DROP TABLE pipeline_run_invocations;
DROP FUNCTION guard_retained_run_invocation();
