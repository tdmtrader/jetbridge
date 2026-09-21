DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_output_holds) OR EXISTS (SELECT 1 FROM pipeline_run_output_finishes) THEN
        RAISE EXCEPTION 'cannot remove retained Run output evidence';
    END IF;
END $$;
DROP TRIGGER run_output_disposition_match ON hangar_handoff_dispositions;
DROP TRIGGER run_output_disposition_match ON pipeline_run_output_finishes;
DROP FUNCTION check_run_output_disposition();
DROP TABLE pipeline_run_output_finishes;
DROP TABLE pipeline_run_output_holds;
DROP FUNCTION immutable_run_output_evidence();
