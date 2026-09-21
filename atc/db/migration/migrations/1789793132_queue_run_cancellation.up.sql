ALTER TABLE pipeline_run_cancellation_worker
 ADD COLUMN last_run_id bigint NOT NULL DEFAULT 0 CHECK(last_run_id>=0),
 ADD COLUMN run_high_water bigint NOT NULL DEFAULT 0 CHECK(run_high_water>=last_run_id);

CREATE TABLE pipeline_run_cancellation_progress (
 run_id bigint PRIMARY KEY REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
 next_discovery_kind integer NOT NULL DEFAULT 0 CHECK(next_discovery_kind BETWEEN 0 AND 7),
 next_claim_kind integer NOT NULL DEFAULT 0 CHECK(next_claim_kind BETWEEN 0 AND 7)
);
CREATE TABLE pipeline_run_cancellation_operations (
 id bigserial PRIMARY KEY,
 run_id bigint NOT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
 kind text NOT NULL CHECK(kind IN ('scheduler_debt_close','handoff_classify',
  'capture_cancel_or_settle','source_hold_release','build_abort',
  'executor_finish_or_stop_ack','candidate_settle','terminalize')),
 subject text NOT NULL CHECK(octet_length(subject) BETWEEN 1 AND 256),
 attempt_count bigint NOT NULL DEFAULT 0 CHECK(attempt_count>=0),
 worker_epoch bigint CHECK(worker_epoch>0),
 claimed boolean NOT NULL DEFAULT false,
 next_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 debt text NOT NULL DEFAULT '' CHECK(debt IN ('','interrupted','pending','unavailable','timeout','conflict')),
 completed_at timestamptz,
 UNIQUE(run_id,kind,subject),
 CHECK(NOT claimed OR (worker_epoch IS NOT NULL AND attempt_count>0 AND completed_at IS NULL AND debt='interrupted')),
 CHECK(completed_at IS NULL OR (NOT claimed AND debt=''))
);
CREATE INDEX pipeline_run_cancellation_due ON pipeline_run_cancellation_operations(run_id,kind,id) WHERE completed_at IS NULL;
CREATE TABLE pipeline_run_cancellation_cursors (
 run_id bigint NOT NULL REFERENCES pipeline_run_cancellation_progress(run_id),
 kind text NOT NULL,
 after_id bigint NOT NULL DEFAULT 0 CHECK(after_id>=0),
 high_water bigint NOT NULL DEFAULT 0 CHECK(high_water>=after_id),
 cycle bigint NOT NULL DEFAULT 0 CHECK(cycle>=0),
 PRIMARY KEY(run_id,kind)
);
CREATE FUNCTION immutable_run_cancellation_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.run_id<>OLD.run_id OR NEW.kind<>OLD.kind OR NEW.subject<>OLD.subject THEN
  RAISE EXCEPTION 'Run cancellation work identity is immutable until Run purge is integrated';
 END IF;
 IF OLD.completed_at IS NOT NULL AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'completed Run cancellation work is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER run_cancellation_operation_immutable BEFORE UPDATE OR DELETE ON pipeline_run_cancellation_operations
 FOR EACH ROW EXECUTE FUNCTION immutable_run_cancellation_operation();
