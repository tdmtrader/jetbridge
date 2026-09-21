DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_definitions) THEN
        RAISE EXCEPTION 'cannot discard retained Run definitions';
    END IF;
END; $$;
DROP TABLE pipeline_run_definitions;
DROP FUNCTION guard_retained_run_definition();
