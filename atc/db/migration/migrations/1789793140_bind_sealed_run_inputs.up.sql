ALTER TABLE pipeline_run_inputs
    ALTER COLUMN source_run_id DROP NOT NULL,
    ALTER COLUMN source_result DROP NOT NULL,
    ADD COLUMN source_id text,
    ADD CONSTRAINT run_input_source_kind CHECK (
        (source_id IS NULL AND source_run_id IS NOT NULL AND source_result IS NOT NULL)
        OR
        (source_id IS NOT NULL AND source_id ~ '^input-v1-[0-9a-f]{64}$'
         AND source_run_id IS NULL AND source_result IS NULL)
    );
