-- An execution is admitted on a node before its step pod exists, and the pod
-- is pinned to that node by name. When the pod is never bound -- the node is
-- full, gone or unreachable -- the execution may move to another node: its
-- node is the only thing that changes, and only while nothing has been
-- started or closed under its identity. Everything else about an admission,
-- and every node of an execution that has started, stays immutable.
--
-- The caller is what proves the pod never ran: it deletes the pod with a
-- precondition on an observation that shows it unbound. The database half is
-- that a start or a closure, once retained, pins the node for good.
CREATE FUNCTION run_execution_moves_only_unstarted() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    unmoved pipeline_run_executions := NEW;
BEGIN
    unmoved.node_name := OLD.node_name;
    unmoved.node_uid := OLD.node_uid;
    IF unmoved IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Run output evidence is immutable';
    END IF;
    IF (NEW.node_name, NEW.node_uid) IS NOT DISTINCT FROM (OLD.node_name, OLD.node_uid) THEN
        RETURN NEW;
    END IF;
    IF EXISTS (SELECT 1 FROM pipeline_run_execution_starts s
               WHERE s.execution_id = OLD.execution_id AND s.execution_fence = OLD.execution_fence)
       OR EXISTS (SELECT 1 FROM pipeline_run_execution_closures c
               WHERE c.execution_id = OLD.execution_id AND c.execution_fence = OLD.execution_fence) THEN
        RAISE EXCEPTION 'Run execution % was started or closed on node %; its node is immutable',
            OLD.execution_id, OLD.node_name;
    END IF;
    RETURN NEW;
END $$;

DROP TRIGGER run_execution_identity_immutable ON pipeline_run_executions;
CREATE TRIGGER run_execution_identity_immutable BEFORE DELETE ON pipeline_run_executions
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();
CREATE TRIGGER run_execution_moves_only_unstarted BEFORE UPDATE ON pipeline_run_executions
    FOR EACH ROW EXECUTE FUNCTION run_execution_moves_only_unstarted();

-- A capturing execution's capture row names the same node, and moves with it:
-- only while it is pending and no pod has been bound to it. Every other part
-- of the capture's identity stays immutable.
CREATE OR REPLACE FUNCTION hangar_capture_transition() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'hangar: a capture row is deleted only by its team''s purge'
            USING ERRCODE = 'JB001';
    END IF;
    IF (NEW.execution_id, NEW.execution_fence, NEW.output_name, NEW.capture_deadline_at, NEW.created_at)
       IS DISTINCT FROM (OLD.execution_id, OLD.execution_fence, OLD.output_name, OLD.capture_deadline_at, OLD.created_at) THEN
        RAISE EXCEPTION 'hangar: capture %/% identity is immutable', OLD.execution_id, OLD.output_name
            USING ERRCODE = 'JB001';
    END IF;
    IF (NEW.node, NEW.node_uid) IS DISTINCT FROM (OLD.node, OLD.node_uid) AND NOT (
        OLD.state = 'pending' AND NEW.state = 'pending' AND OLD.pod_uid IS NULL AND NEW.pod_uid IS NULL) THEN
        RAISE EXCEPTION 'hangar: capture %/% identity is immutable: it moves node only while pending and bound to no pod',
            OLD.execution_id, OLD.output_name
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.pod_uid IS NOT NULL AND NEW.pod_uid IS DISTINCT FROM OLD.pod_uid THEN
        RAISE EXCEPTION 'hangar: capture %/% is bound to pod %', OLD.execution_id, OLD.output_name, OLD.pod_uid
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.digest IS NOT NULL AND (NEW.scope, NEW.digest) IS DISTINCT FROM (OLD.scope, OLD.digest) THEN
        RAISE EXCEPTION 'hangar: capture %/% resolved to %/%; a resolved digest never moves',
            OLD.execution_id, OLD.output_name, OLD.scope, OLD.digest
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.state <> OLD.state AND NOT (
        (OLD.state = 'pending' AND NEW.state IN ('publishing', 'discarded', 'failed')) OR
        (OLD.state = 'publishing' AND NEW.state IN ('published', 'failed'))) THEN
        RAISE EXCEPTION 'hangar: capture %/% cannot move from % to %',
            OLD.execution_id, OLD.output_name, OLD.state, NEW.state
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.state IN ('published', 'discarded', 'failed') AND
       (NEW.generation, NEW.error, NEW.finished_at) IS DISTINCT FROM (OLD.generation, OLD.error, OLD.finished_at) THEN
        RAISE EXCEPTION 'hangar: capture %/% is % and terminal', OLD.execution_id, OLD.output_name, OLD.state
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.released_at IS NOT NULL AND NEW.released_at IS DISTINCT FROM OLD.released_at THEN
        RAISE EXCEPTION 'hangar: capture %/% was released at %; release is one-way',
            OLD.execution_id, OLD.output_name, OLD.released_at
            USING ERRCODE = 'JB001';
    END IF;
    RETURN NEW;
END $$;
