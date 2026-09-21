CREATE TABLE pipeline_run_execution_starts (
 execution_id uuid NOT NULL,
 execution_fence bigint NOT NULL,
 witness jsonb NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(execution_id,execution_fence),
 FOREIGN KEY(execution_id,execution_fence) REFERENCES pipeline_run_executions(execution_id,execution_fence)
);
CREATE TABLE pipeline_run_execution_closures (
 execution_id uuid NOT NULL,
 execution_fence bigint NOT NULL,
 classification text NOT NULL CHECK(classification IN ('authoritative_finish','authoritative_stop','never_started')),
 observation jsonb NOT NULL,
 worker_epoch bigint CHECK(worker_epoch>0),
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(execution_id,execution_fence),
 FOREIGN KEY(execution_id,execution_fence) REFERENCES pipeline_run_executions(execution_id,execution_fence),
 CHECK(classification<>'never_started' OR worker_epoch IS NOT NULL)
);
CREATE TRIGGER run_execution_start_immutable BEFORE UPDATE OR DELETE ON pipeline_run_execution_starts
 FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();
CREATE TRIGGER run_execution_closure_immutable BEFORE UPDATE OR DELETE ON pipeline_run_execution_closures
 FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

CREATE FUNCTION check_run_execution_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
 closed text;
 started boolean;
BEGIN
 SELECT classification INTO closed FROM pipeline_run_execution_closures
 WHERE execution_id=NEW.execution_id AND execution_fence=NEW.execution_fence;
 SELECT EXISTS(SELECT 1 FROM pipeline_run_execution_starts
 WHERE execution_id=NEW.execution_id AND execution_fence=NEW.execution_fence) INTO started;
 IF (closed='never_started' AND started) OR (closed IN ('authoritative_finish','authoritative_stop') AND NOT started) THEN
  RAISE EXCEPTION 'Run execution closure contradicts its retained start';
 END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_execution_start_consistent AFTER INSERT ON pipeline_run_execution_starts
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_execution_evidence();
CREATE CONSTRAINT TRIGGER run_execution_closure_consistent AFTER INSERT ON pipeline_run_execution_closures
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_execution_evidence();

CREATE FUNCTION run_execution_closed(build integer) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM pipeline_run_executions e
 LEFT JOIN pipeline_run_execution_closures c USING(execution_id,execution_fence)
 WHERE e.build_id=build AND c.execution_id IS NULL);
$$;
