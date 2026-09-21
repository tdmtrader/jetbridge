DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE source_requested_at IS NOT NULL) THEN
        RAISE EXCEPTION 'cannot remove committed Run source dispatch evidence';
    END IF;
END $$;
CREATE OR REPLACE FUNCTION immutable_run_output_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Run output start identity is immutable until Run purge is integrated';
    END IF;
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Run output start identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
ALTER TABLE pipeline_run_output_starts DROP COLUMN source_requested_at;
