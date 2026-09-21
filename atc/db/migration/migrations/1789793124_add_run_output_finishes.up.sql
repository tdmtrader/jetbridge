-- Run evidence is retained with its immutable start identity, independently of
-- disposable builds. None of these rows is authority to seal or publish bytes.
CREATE TABLE pipeline_run_output_holds (
    handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_starts(handoff_id),
    acknowledgement jsonb NOT NULL
);
CREATE TABLE pipeline_run_output_finishes (
    handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_starts(handoff_id),
    disposition text NOT NULL CHECK (disposition IN ('capture', 'no_capture', 'pre_reservation_cancel')),
    decision jsonb NOT NULL,
    producer_checkpoint_id text UNIQUE,
    reservation_id uuid UNIQUE REFERENCES hangar_capture_reservations(reservation_id),
    CHECK ((disposition = 'capture') = (producer_checkpoint_id IS NOT NULL)),
    CHECK ((disposition = 'capture') = (reservation_id IS NOT NULL))
);
CREATE FUNCTION immutable_run_output_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Run output evidence is immutable until Run purge is integrated';
    END IF;
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Run output evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER run_output_hold_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_holds
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();
CREATE TRIGGER run_output_finish_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_finishes
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

-- Even a generic repository caller cannot commit a Run's Hangar decision
-- without the matching Run checkpoint in the same transaction.
CREATE FUNCTION check_run_output_disposition() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    decision pipeline_run_output_finishes%ROWTYPE;
    branch text;
    reservation hangar_capture_reservations%ROWTYPE;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE handoff_id=NEW.handoff_id) THEN
        RETURN NULL;
    END IF;
    SELECT * INTO decision FROM pipeline_run_output_finishes WHERE handoff_id=NEW.handoff_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'Run output disposition has no owning Run decision';
    END IF;
    SELECT disposition INTO branch FROM hangar_handoff_dispositions WHERE handoff_id=NEW.handoff_id;
    IF branch IS DISTINCT FROM decision.disposition THEN
        RAISE EXCEPTION 'Run and Hangar output dispositions differ';
    END IF;
    IF branch = 'capture' THEN
        SELECT * INTO reservation FROM hangar_capture_reservations WHERE handoff_id=NEW.handoff_id;
        IF NOT FOUND OR reservation.reservation_id IS DISTINCT FROM decision.reservation_id
           OR reservation.producer_checkpoint_id IS DISTINCT FROM decision.producer_checkpoint_id THEN
            RAISE EXCEPTION 'Run producer checkpoint does not match the capture reservation';
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_output_disposition_match AFTER INSERT ON pipeline_run_output_finishes
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_disposition();
CREATE CONSTRAINT TRIGGER run_output_disposition_match AFTER INSERT ON hangar_handoff_dispositions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_disposition();
