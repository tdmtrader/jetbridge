DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_inputs WHERE source_id IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot downgrade while retained sealed-input bindings exist';
    END IF;
END $$;
ALTER TABLE pipeline_run_inputs
    DROP CONSTRAINT run_input_source_kind,
    DROP COLUMN source_id,
    ALTER COLUMN source_run_id SET NOT NULL,
    ALTER COLUMN source_result SET NOT NULL;
