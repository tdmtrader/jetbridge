-- Run evidence is immutable and undeletable, with exactly one exception: the
-- purge of the team that owns the Run. The purge marker is the same
-- transaction-local setting the payload delete guard (1773105509) honours, so
-- it never outlives the purging transaction or leaks to a pooled session.
CREATE FUNCTION run_team_purge_active() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(current_setting('concourse.pipeline_run_team_purge', true), '') = 'on';
$$;

-- Check collection is the one other, narrower exception. A check produces no
-- Run result, so its execution evidence is load-bearing only while the
-- execution could still be interrupted or fenced. Once it has a closure and
-- its build is complete, the evidence is inert and may go with the build.
-- That holds for a Run check build only (no run job name): a job build's
-- image check is job evidence. A check whose custom image is fetched by a get
-- runs that get inside the check build, so a closed get of a Run check build
-- is inert too, unless a Run output start names the build or the execution.
-- The marker is transaction-local, like the purge marker.
CREATE FUNCTION run_check_gc_active() RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce(current_setting('concourse.pipeline_run_check_gc', true), '') = 'on';
$$;

-- A collectable check execution takes its start and closure witnesses with
-- it. The execution's own guard decides; the witnesses' guards admit only the
-- cascade (their execution is already gone), never a direct delete.
DO $$
DECLARE fk record;
BEGIN
 FOR fk IN SELECT conrelid::regclass AS tbl, conname FROM pg_constraint
  WHERE contype='f' AND confrelid='pipeline_run_executions'::regclass
    AND conrelid IN ('pipeline_run_execution_starts'::regclass, 'pipeline_run_execution_closures'::regclass)
 LOOP
  EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', fk.tbl, fk.conname);
 END LOOP;
END $$;
ALTER TABLE pipeline_run_execution_starts ADD CONSTRAINT pipeline_run_execution_starts_execution_fkey
 FOREIGN KEY(execution_id,execution_fence) REFERENCES pipeline_run_executions(execution_id,execution_fence) ON DELETE CASCADE;
ALTER TABLE pipeline_run_execution_closures ADD CONSTRAINT pipeline_run_execution_closures_execution_fkey
 FOREIGN KEY(execution_id,execution_fence) REFERENCES pipeline_run_executions(execution_id,execution_fence) ON DELETE CASCADE;

CREATE OR REPLACE FUNCTION immutable_run_output_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        IF run_check_gc_active() THEN
            -- Nested: OLD's columns differ by table, so each test is reached
            -- only for the table that has them.
            IF TG_TABLE_NAME = 'pipeline_run_executions' THEN
                IF OLD.kind IN ('check', 'get') AND OLD.handoff_id IS NULL
                   AND EXISTS (SELECT 1 FROM pipeline_run_execution_closures c
                       WHERE c.execution_id = OLD.execution_id AND c.execution_fence = OLD.execution_fence)
                   AND EXISTS (SELECT 1 FROM builds b WHERE b.id = OLD.build_id AND b.completed
                       AND b.pipeline_run_id IS NOT NULL AND b.run_job_name IS NULL)
                   AND NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts s WHERE s.build_id = OLD.build_id) THEN
                    RETURN OLD;
                END IF;
            ELSIF TG_TABLE_NAME IN ('pipeline_run_execution_starts', 'pipeline_run_execution_closures') THEN
                IF NOT EXISTS (SELECT 1 FROM pipeline_run_executions e
                       WHERE e.execution_id = OLD.execution_id AND e.execution_fence = OLD.execution_fence) THEN
                    RETURN OLD;
                END IF;
            END IF;
            RAISE EXCEPTION 'check collection deletes only a closed check or image get execution of a completed Run check build, with its witnesses';
        END IF;
        RAISE EXCEPTION 'Run evidence is deleted only by its team''s purge';
    END IF;
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Run output evidence is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION immutable_run_output_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'Run output start identity is deleted only by its team''s purge';
    END IF;
    IF (to_jsonb(NEW) - 'source_requested_at') IS DISTINCT FROM (to_jsonb(OLD) - 'source_requested_at')
       OR (OLD.source_requested_at IS NOT NULL AND NEW.source_requested_at IS DISTINCT FROM OLD.source_requested_at) THEN
        RAISE EXCEPTION 'Run output start identity and committed source dispatch are immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION immutable_run_cancellation_operation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF run_team_purge_active() THEN
   RETURN OLD;
  END IF;
  RAISE EXCEPTION 'Run cancellation work is deleted only by its team''s purge';
 END IF;
 IF NEW.id<>OLD.id OR NEW.run_id<>OLD.run_id OR NEW.kind<>OLD.kind OR NEW.subject<>OLD.subject THEN
  RAISE EXCEPTION 'Run cancellation work identity is immutable';
 END IF;
 IF OLD.completed_at IS NOT NULL AND NEW IS DISTINCT FROM OLD THEN
  RAISE EXCEPTION 'completed Run cancellation work is immutable';
 END IF;
 RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION immutable_run_credential_handoff() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'A Run credential handoff cannot be reseeded';
    END IF;
    IF TG_OP = 'UPDATE' AND (
        (NEW.run_id, NEW.handoff_id, NEW.claimed_at) IS DISTINCT FROM (OLD.run_id, OLD.handoff_id, OLD.claimed_at)
        OR (OLD.ready_at IS NOT NULL AND NEW.ready_at IS DISTINCT FROM OLD.ready_at)
    ) THEN
        RAISE EXCEPTION 'Run credential delivery identity and readiness are immutable';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts s JOIN pipeline_run_invocations i ON i.run_id=s.run_id
        WHERE s.handoff_id=NEW.handoff_id AND s.run_id=NEW.run_id) THEN
        RAISE EXCEPTION 'A credential handoff must belong to its invoked Run';
    END IF;
    RETURN NEW;
END $$;

-- An upload's claim is released by the purge in the same transaction; the
-- purge does not wait for its deadline.
CREATE OR REPLACE FUNCTION pipeline_run_input_upload_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        IF OLD.expires_at > clock_timestamp() OR EXISTS (
            SELECT 1 FROM hangar_claims WHERE claim_id=OLD.claim_id AND released_at IS NULL
        ) THEN
            RAISE EXCEPTION 'Run input upload must expire and release its claim before cleanup' USING ERRCODE = 'JB001';
        END IF;
        RETURN OLD;
    END IF;
    IF (NEW.reservation_id, NEW.template_pipeline_id, NEW.team_id, NEW.principal_digest,
        NEW.input_name, NEW.activation_epoch, NEW.claim_id, NEW.created_at, NEW.expires_at)
        IS DISTINCT FROM
        (OLD.reservation_id, OLD.template_pipeline_id, OLD.team_id, OLD.principal_digest,
        OLD.input_name, OLD.activation_epoch, OLD.claim_id, OLD.created_at, OLD.expires_at)
        OR (OLD.claim_acquired_at IS NOT NULL AND NEW.claim_acquired_at IS DISTINCT FROM OLD.claim_acquired_at) THEN
        RAISE EXCEPTION 'Run input upload identity and deadline are immutable' USING ERRCODE = 'JB001';
    END IF;
    RETURN NEW;
END $$;

-- The definition and invocation go with the header's cascade; under a purge
-- they do not depend on the cascade's visibility of the deleted header.
CREATE OR REPLACE FUNCTION guard_retained_run_definition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT run_team_purge_active() AND EXISTS (SELECT 1 FROM pipeline_runs WHERE id = OLD.run_id) THEN
            RAISE EXCEPTION 'definition is retained for the lifetime of its Run';
        END IF;
        RETURN OLD;
    END IF;
    IF NEW.run_id IS DISTINCT FROM OLD.run_id
       OR NEW.template_digest IS DISTINCT FROM OLD.template_digest THEN
        RAISE EXCEPTION 'retained Run definition identity and attestation are immutable';
    END IF;
    -- config/nonce may be re-encrypted by the database key-rotation machinery.
    -- Every read verifies the plaintext against these immutable attestations:
    -- template_digest here and pipeline_runs.config_hash on the owning header.
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION guard_retained_run_invocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF NOT run_team_purge_active() AND EXISTS (SELECT 1 FROM pipeline_runs WHERE id = OLD.run_id) THEN
            RAISE EXCEPTION 'invocation is retained for the lifetime of its Run';
        END IF;
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'retained Run invocation is immutable';
END;
$$;
