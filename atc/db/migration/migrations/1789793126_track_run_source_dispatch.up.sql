ALTER TABLE pipeline_run_output_starts ADD COLUMN source_requested_at timestamptz;
CREATE OR REPLACE FUNCTION immutable_run_output_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Run output start identity is immutable until Run purge is integrated';
    END IF;
    IF (to_jsonb(NEW) - 'source_requested_at') IS DISTINCT FROM (to_jsonb(OLD) - 'source_requested_at')
       OR (OLD.source_requested_at IS NOT NULL AND NEW.source_requested_at IS DISTINCT FROM OLD.source_requested_at) THEN
        RAISE EXCEPTION 'Run output start identity and committed source dispatch are immutable';
    END IF;
    RETURN NEW;
END;
$$;
-- A source already durably recorded by the prior version proves dispatch.
UPDATE pipeline_run_output_starts s SET source_requested_at=h.reserved_at
    FROM hangar_handoff_predeclarations h WHERE h.handoff_id=s.handoff_id AND h.reserved_at IS NOT NULL;
