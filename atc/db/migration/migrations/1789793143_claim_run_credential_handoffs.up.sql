-- Delivery facts only. Credentials, paths and bearer tokens never enter this table.
CREATE TABLE pipeline_run_credential_handoffs (
    run_id bigint PRIMARY KEY REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
    handoff_id uuid NOT NULL UNIQUE REFERENCES pipeline_run_output_starts(handoff_id) ON DELETE RESTRICT,
    claimed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    ready_at timestamptz,
    CHECK (ready_at IS NULL OR ready_at >= claimed_at)
);

CREATE FUNCTION immutable_run_credential_handoff() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'A Run credential handoff cannot be reseeded';
    END IF;
    IF TG_OP = 'UPDATE' AND (
        (NEW.run_id, NEW.handoff_id, NEW.claimed_at) IS DISTINCT FROM (OLD.run_id, OLD.handoff_id, OLD.claimed_at)
        OR (OLD.ready_at IS NOT NULL AND NEW.ready_at IS DISTINCT FROM OLD.ready_at)
    ) THEN
        RAISE EXCEPTION 'Run credential delivery identity and readiness are immutable';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts s JOIN pipeline_run_invocations i ON i.run_id=s.run_id
        WHERE s.handoff_id=NEW.handoff_id AND s.run_id=NEW.run_id) THEN
        RAISE EXCEPTION 'A credential handoff must belong to its invoked Run';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER immutable_run_credential_handoff BEFORE INSERT OR UPDATE OR DELETE ON pipeline_run_credential_handoffs
    FOR EACH ROW EXECUTE FUNCTION immutable_run_credential_handoff();
