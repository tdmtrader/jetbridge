DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_input_uploads) THEN
        RAISE EXCEPTION 'cannot downgrade while temporary Run input uploads exist';
    END IF;
END $$;
DROP TABLE pipeline_run_input_uploads;
DROP FUNCTION pipeline_run_input_upload_claim();
DROP FUNCTION pipeline_run_input_upload_immutable();
