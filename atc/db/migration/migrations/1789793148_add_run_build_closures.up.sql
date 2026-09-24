-- A build closure is an aborted Run build's own request to close the work it
-- left open -- its output handoffs and its execution -- without cancelling its
-- Run. The cancellation worker converges it; it is open until closed_at is set.
CREATE TABLE pipeline_run_build_closures (
 build_id integer PRIMARY KEY REFERENCES builds(id) ON DELETE CASCADE,
 run_id integer NOT NULL REFERENCES pipeline_runs(id) ON DELETE CASCADE,
 requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 closed_at timestamptz,
 CHECK (closed_at IS NULL OR closed_at >= requested_at)
);
CREATE INDEX pipeline_run_build_closures_open ON pipeline_run_build_closures(run_id) WHERE closed_at IS NULL;

CREATE FUNCTION immutable_run_build_closure() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.build_id IS DISTINCT FROM OLD.build_id OR NEW.run_id IS DISTINCT FROM OLD.run_id OR
  NEW.requested_at IS DISTINCT FROM OLD.requested_at OR
  (OLD.closed_at IS NOT NULL AND NEW.closed_at IS DISTINCT FROM OLD.closed_at) THEN
  RAISE EXCEPTION 'a build closure is immutable once requested, and closes once';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER run_build_closure_immutable BEFORE UPDATE ON pipeline_run_build_closures
 FOR EACH ROW EXECUTE FUNCTION immutable_run_build_closure();
