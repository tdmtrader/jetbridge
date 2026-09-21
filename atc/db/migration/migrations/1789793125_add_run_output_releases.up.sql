CREATE TABLE pipeline_run_output_releases (
    handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_finishes(handoff_id),
    acknowledgement jsonb NOT NULL
);
CREATE TRIGGER run_output_release_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_releases
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

CREATE FUNCTION check_run_output_release() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    handoff uuid := NEW.handoff_id;
    retained jsonb;
    generic jsonb;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE handoff_id=handoff) THEN
        RETURN NULL;
    END IF;
    SELECT acknowledgement INTO retained FROM pipeline_run_output_releases WHERE handoff_id=handoff;
    SELECT coalesce(c.release_acknowledgement,n.release_acknowledgement,x.release_acknowledgement)
      INTO generic FROM hangar_handoff_predeclarations h
      LEFT JOIN hangar_capture_reservations c USING(handoff_id)
      LEFT JOIN hangar_no_capture_dispositions n USING(handoff_id)
      LEFT JOIN hangar_pre_reservation_cancel_dispositions x USING(handoff_id)
      WHERE h.handoff_id=handoff;
    IF generic IS DISTINCT FROM retained THEN
        RAISE EXCEPTION 'Run and Hangar source release acknowledgements must commit together';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_output_release_match AFTER INSERT ON pipeline_run_output_releases
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_release();
CREATE CONSTRAINT TRIGGER run_output_release_match AFTER UPDATE OF release_acknowledgement ON hangar_capture_reservations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_release();
CREATE CONSTRAINT TRIGGER run_output_release_match AFTER UPDATE OF release_acknowledgement ON hangar_no_capture_dispositions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_release();
CREATE CONSTRAINT TRIGGER run_output_release_match AFTER UPDATE OF release_acknowledgement ON hangar_pre_reservation_cancel_dispositions
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_release();
