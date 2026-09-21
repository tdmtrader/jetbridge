DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_output_candidates) OR EXISTS (SELECT 1 FROM pipeline_run_output_discards) THEN
        RAISE EXCEPTION 'Run candidates must be retained until governed Run purge';
    END IF;
END $$;
DROP TRIGGER run_output_candidate_match ON hangar_capture_reservations;
DROP TABLE pipeline_run_output_candidates;
DROP TABLE pipeline_run_output_discards;
DROP FUNCTION check_run_output_candidate();
