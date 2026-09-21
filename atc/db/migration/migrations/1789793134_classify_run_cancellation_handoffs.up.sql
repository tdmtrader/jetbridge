CREATE TABLE pipeline_run_output_cancellation_classifications (
 handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_starts(handoff_id),
 worker_epoch bigint NOT NULL CHECK(worker_epoch>0),
 classification text NOT NULL CHECK(classification IN ('never_started','executing','authoritative_finish','authoritative_stop')),
 hold_acknowledged boolean NOT NULL,
 disposition text CHECK(disposition IN ('capture','no_capture','pre_reservation_cancel')),
 reservation_id uuid,
 observation jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((disposition IS NOT DISTINCT FROM 'capture')=(reservation_id IS NOT NULL))
);
CREATE TRIGGER run_output_cancellation_classification_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_cancellation_classifications
 FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

-- Existing retained terminal evidence remains valid. New cleanup work records
-- classification before closing or interrupting start, then retains its outcome.
CREATE FUNCTION check_run_cancel_classified() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=NEW.handoff_id) THEN
  RAISE EXCEPTION 'Run cancellation outcome requires prior handoff classification';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_cancel_classified AFTER INSERT ON pipeline_run_output_cancellation_evidence
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_cancel_classified();
