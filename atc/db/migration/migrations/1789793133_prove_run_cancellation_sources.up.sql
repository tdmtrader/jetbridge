-- A cancellation request is not an exact execution outcome. Keep the evidence
-- independently of both a disposable build and the cleanup queue's retry state.
CREATE TABLE pipeline_run_output_cancellation_evidence (
 handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_starts(handoff_id),
 worker_epoch bigint NOT NULL CHECK(worker_epoch>0),
 classification text NOT NULL CHECK(classification IN ('never_started','authoritative_finish','authoritative_stop')),
 evidence jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER run_output_cancellation_evidence_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_cancellation_evidence
 FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

CREATE FUNCTION check_run_cancel_source_proof() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.disposition='pre_reservation_cancel' AND
    EXISTS(SELECT 1 FROM hangar_handoff_predeclarations WHERE handoff_id=NEW.handoff_id AND reserved_at IS NOT NULL) AND
    NOT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=NEW.handoff_id) THEN
  RAISE EXCEPTION 'Run source cancellation requires exact execution evidence';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_cancel_source_proof AFTER INSERT ON pipeline_run_output_finishes
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_cancel_source_proof();
