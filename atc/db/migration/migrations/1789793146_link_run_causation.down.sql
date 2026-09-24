DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_runs WHERE caused_by_run IS NOT NULL OR correlation IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot discard retained Run causation or correlation';
    END IF;
END; $$;
DROP TRIGGER run_causation_immutable ON pipeline_runs;
DROP FUNCTION immutable_run_causation();
ALTER TABLE pipeline_runs
    DROP CONSTRAINT pipeline_run_correlation,
    DROP CONSTRAINT pipeline_run_causation,
    DROP COLUMN correlation,
    DROP COLUMN caused_by_run;
