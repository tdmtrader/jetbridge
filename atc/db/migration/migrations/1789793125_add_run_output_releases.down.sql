DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_output_releases) THEN
        RAISE EXCEPTION 'cannot remove retained Run output releases';
    END IF;
END $$;
DROP TRIGGER run_output_release_match ON hangar_capture_reservations;
DROP TRIGGER run_output_release_match ON hangar_no_capture_dispositions;
DROP TRIGGER run_output_release_match ON hangar_pre_reservation_cancel_dispositions;
DROP TRIGGER run_output_release_match ON pipeline_run_output_releases;
DROP FUNCTION check_run_output_release();
DROP TABLE pipeline_run_output_releases;
