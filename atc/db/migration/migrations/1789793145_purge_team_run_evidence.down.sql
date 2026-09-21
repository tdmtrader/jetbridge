ALTER TABLE pipeline_run_execution_starts DROP CONSTRAINT pipeline_run_execution_starts_execution_fkey,
 ADD CONSTRAINT pipeline_run_execution_starts_execution_fkey
 FOREIGN KEY(execution_id,execution_fence) REFERENCES pipeline_run_executions(execution_id,execution_fence);
ALTER TABLE pipeline_run_execution_closures DROP CONSTRAINT pipeline_run_execution_closures_execution_fkey,
 ADD CONSTRAINT pipeline_run_execution_closures_execution_fkey
 FOREIGN KEY(execution_id,execution_fence) REFERENCES pipeline_run_executions(execution_id,execution_fence);

CREATE OR REPLACE FUNCTION immutable_run_output_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Run output evidence is immutable until Run purge is integrated';
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
        RAISE EXCEPTION 'Run output start identity is immutable until Run purge is integrated';
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
 IF TG_OP='DELETE' OR NEW.id<>OLD.id OR NEW.run_id<>OLD.run_id OR NEW.kind<>OLD.kind OR NEW.subject<>OLD.subject THEN
  RAISE EXCEPTION 'Run cancellation work identity is immutable until Run purge is integrated';
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

CREATE OR REPLACE FUNCTION pipeline_run_input_upload_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
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

CREATE OR REPLACE FUNCTION guard_retained_run_definition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM pipeline_runs WHERE id = OLD.run_id) THEN
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
        IF EXISTS (SELECT 1 FROM pipeline_runs WHERE id = OLD.run_id) THEN
            RAISE EXCEPTION 'invocation is retained for the lifetime of its Run';
        END IF;
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'retained Run invocation is immutable';
END;
$$;

DROP FUNCTION run_check_gc_active();
DROP FUNCTION run_team_purge_active();
