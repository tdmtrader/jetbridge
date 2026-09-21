ALTER TABLE pipeline_runs
 ADD COLUMN cancel_requested_at timestamptz,
 ADD COLUMN cancel_requested_by text,
 ADD COLUMN cancel_reason text,
 ADD CONSTRAINT run_cancel_request_shape CHECK (
  (cancel_requested_at IS NULL AND cancel_requested_by IS NULL AND cancel_reason IS NULL) OR
  (run_contract_version='v2' AND cancel_requested_at IS NOT NULL AND cancel_requested_by IS NOT NULL
   AND cancel_requested_by<>'' AND status IN ('running','aborted')
   AND (cancel_reason IS NULL OR octet_length(cancel_reason) BETWEEN 1 AND 512)));

CREATE FUNCTION immutable_run_cancellation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.cancel_requested_at IS NOT NULL AND
  (NEW.cancel_requested_at IS DISTINCT FROM OLD.cancel_requested_at OR
   NEW.cancel_requested_by IS DISTINCT FROM OLD.cancel_requested_by OR
   NEW.cancel_reason IS DISTINCT FROM OLD.cancel_reason) THEN
  RAISE EXCEPTION 'Run cancellation request is immutable';
 END IF;
 IF OLD.status<>'running' AND OLD.cancel_requested_at IS NULL AND NEW.cancel_requested_at IS NOT NULL THEN
  RAISE EXCEPTION 'Run terminal publication precedes cancellation';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER run_cancellation_immutable BEFORE UPDATE ON pipeline_runs
 FOR EACH ROW EXECUTE FUNCTION immutable_run_cancellation();

ALTER TABLE pipeline_run_output_discards DROP CONSTRAINT pipeline_run_output_discards_reason_check,
 ADD CONSTRAINT pipeline_run_output_discards_reason_check CHECK(reason IN ('build_aborted','run_cancelled'));
