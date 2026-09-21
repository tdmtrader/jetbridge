-- Attribution only: the node execution ledger remains authoritative for start,
-- finish and stop. No provider credential or process state is stored here.
CREATE TABLE pipeline_run_executions (
 run_id integer NOT NULL REFERENCES pipeline_runs(id),
 build_id integer NOT NULL REFERENCES builds(id),
 plan_id text NOT NULL CHECK(length(plan_id) BETWEEN 1 AND 256),
 kind text NOT NULL CHECK(kind IN ('task','get','put','check')),
 activation_epoch bigint NOT NULL CHECK(activation_epoch>0),
 node_name text NOT NULL CHECK(node_name<>''),
 node_uid text NOT NULL CHECK(node_uid<>''),
 execution_id uuid NOT NULL,
 execution_fence bigint NOT NULL CHECK(execution_fence>0),
 handoff_id uuid UNIQUE REFERENCES pipeline_run_output_starts(handoff_id),
 admitted_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(build_id,plan_id),
 UNIQUE(execution_id,execution_fence)
);
CREATE INDEX pipeline_run_executions_run ON pipeline_run_executions(run_id);
CREATE TRIGGER run_execution_identity_immutable BEFORE UPDATE OR DELETE ON pipeline_run_executions
 FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();
