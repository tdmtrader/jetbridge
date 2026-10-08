-- Restores the control-key generation columns no_control_key_generation
-- dropped, LOSSILY.
--
-- The generation a row was admitted under is not reconstructible: nothing
-- that survived the up migration records it. Every row comes back as
-- generation 1, which is what the old shape admitted when exactly one
-- control key had ever been configured. The five trigger functions are
-- restored to their 1789793156 bodies verbatim; pipeline_run_executions gets
-- its column-level CHECK back.
--
-- pipeline_runs.activation_epoch (the Run contract's epoch) was never
-- touched and is not touched here.

ALTER TABLE hangar_claims ADD COLUMN activation_epoch bigint NOT NULL DEFAULT 1;
ALTER TABLE hangar_exact_lifecycles ADD COLUMN activation_epoch bigint NOT NULL DEFAULT 1;
ALTER TABLE hangar_input_publications ADD COLUMN activation_epoch bigint NOT NULL DEFAULT 1;
ALTER TABLE pipeline_run_executions ADD COLUMN activation_epoch bigint NOT NULL DEFAULT 1 CHECK (activation_epoch > 0);
ALTER TABLE pipeline_run_input_uploads ADD COLUMN activation_epoch bigint NOT NULL DEFAULT 1;
ALTER TABLE pipeline_run_inputs ADD COLUMN activation_epoch bigint NOT NULL DEFAULT 1;

CREATE OR REPLACE FUNCTION hangar_check_input_publication() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE lifecycle hangar_exact_lifecycles%ROWTYPE;
BEGIN
    -- The node's stage is immutable; neither an old transaction nor a late
    -- commit extends its deadline. Exact committed retries do not update it.
    IF NEW.expires_at <= clock_timestamp() OR NEW.expires_at > clock_timestamp() + interval '2 minutes'
        OR NOT coalesce(NEW.stage->>'version' = 'hangar-input/v1'
            AND NEW.stage->>'reservation_id' = NEW.reservation_id::text
            AND NEW.stage->>'scope' = NEW.scope AND NEW.stage->>'digest' = NEW.digest
            AND (NEW.stage->>'activation_epoch')::bigint = NEW.activation_epoch
            AND (NEW.stage->>'expires_at')::timestamptz = NEW.expires_at, false) THEN
        RAISE EXCEPTION 'hangar: input reservation identity or deadline is invalid' USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lifecycle_id IS NOT NULL THEN
        SELECT * INTO lifecycle FROM hangar_exact_lifecycles WHERE id=NEW.lifecycle_id;
        IF NOT FOUND OR (lifecycle.scope, lifecycle.digest, lifecycle.activation_epoch)
            IS DISTINCT FROM (NEW.scope, NEW.digest, NEW.activation_epoch)
            OR NOT coalesce(NEW.publication->'stage' = NEW.stage
                AND NEW.publication->>'nonce' = NEW.nonce::text
                AND (NEW.publication#>>'{attributes,ref,generation}')::bigint = lifecycle.generation
                AND NEW.publication#>>'{attributes,ref,scope}' = lifecycle.scope
                AND NEW.publication#>>'{attributes,ref,digest}' = lifecycle.digest, false) THEN
            RAISE EXCEPTION 'hangar: input publication differs from its reservation or lifecycle' USING ERRCODE = 'JB001';
        END IF;
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION hangar_input_publication_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'hangar: input publication identities remain tombstoned' USING ERRCODE = 'JB001';
    END IF;
    IF (NEW.reservation_id, NEW.scope, NEW.digest, NEW.activation_epoch, NEW.stage, NEW.nonce, NEW.expires_at)
        IS DISTINCT FROM (OLD.reservation_id, OLD.scope, OLD.digest, OLD.activation_epoch, OLD.stage, OLD.nonce, OLD.expires_at)
        OR (OLD.lifecycle_id IS NOT NULL AND
            (NEW.lifecycle_id, NEW.publication, NEW.registered_at) IS DISTINCT FROM
            (OLD.lifecycle_id, OLD.publication, OLD.registered_at)) THEN
        RAISE EXCEPTION 'hangar: input reservation and registered publication are immutable' USING ERRCODE = 'JB001';
    END IF;
    RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION pipeline_run_input_upload_immutable() RETURNS trigger
    LANGUAGE plpgsql AS $$
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

CREATE OR REPLACE FUNCTION pipeline_run_input_upload_claim() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.expires_at <= clock_timestamp() THEN
        RAISE EXCEPTION 'Run input upload expired before commit' USING ERRCODE = 'JB001';
    END IF;
    IF NEW.claim_acquired_at IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM hangar_claims c JOIN hangar_input_publications p ON p.lifecycle_id=c.lifecycle_id
        WHERE p.reservation_id=NEW.reservation_id AND c.claim_id=NEW.claim_id
            AND c.activation_epoch=NEW.activation_epoch AND c.released_at IS NULL
            AND c.consumer_binding_id=NEW.claim_id::text
    ) THEN
        RAISE EXCEPTION 'Run input upload registration and claim must commit together' USING ERRCODE = 'JB001';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION check_run_input_claim(checked_claim uuid) RETURNS void
    LANGUAGE plpgsql AS $$
DECLARE binding pipeline_run_inputs%ROWTYPE;
BEGIN
    SELECT * INTO binding FROM pipeline_run_inputs WHERE claim_id=checked_claim;
    IF binding.run_id IS NULL THEN RETURN; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM hangar_claims c JOIN hangar_exact_lifecycles l ON l.id=c.lifecycle_id
        JOIN pipeline_runs r ON r.id=binding.run_id
        WHERE c.claim_id=checked_claim AND c.released_at IS NULL
        AND c.consumer_binding_id=checked_claim::text
        AND l.scope=binding.scope AND l.digest=binding.digest AND l.generation=binding.generation
        AND c.activation_epoch=binding.activation_epoch AND l.activation_epoch=binding.activation_epoch
    ) THEN RAISE EXCEPTION 'Run input requires its retained exact-generation claim'; END IF;
END;
$$;
