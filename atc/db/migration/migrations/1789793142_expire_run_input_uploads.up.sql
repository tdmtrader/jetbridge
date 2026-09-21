-- Consumer-owned temporary protection; no bearer or provider credential is
-- retained here. The Run takes its own independent claim during admission.
CREATE TABLE pipeline_run_input_uploads (
    reservation_id uuid PRIMARY KEY REFERENCES hangar_input_publications(reservation_id)
        ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    template_pipeline_id integer NOT NULL REFERENCES pipelines(id) ON DELETE RESTRICT,
    team_id integer NOT NULL REFERENCES teams(id) ON DELETE RESTRICT,
    principal_digest text NOT NULL CHECK (principal_digest ~ '^[0-9a-f]{64}$'),
    input_name text NOT NULL CHECK (input_name <> '' AND octet_length(input_name) <= 256),
    activation_epoch bigint NOT NULL REFERENCES hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT,
    claim_id uuid NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    claim_acquired_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '15 minutes'),
    CHECK (claim_acquired_at IS NULL OR (claim_acquired_at >= created_at AND claim_acquired_at < expires_at))
);
CREATE INDEX pipeline_run_input_upload_expiry ON pipeline_run_input_uploads(expires_at, reservation_id);

CREATE FUNCTION pipeline_run_input_upload_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER pipeline_run_input_upload_immutable BEFORE UPDATE OR DELETE ON pipeline_run_input_uploads
    FOR EACH ROW EXECUTE FUNCTION pipeline_run_input_upload_immutable();

CREATE FUNCTION pipeline_run_input_upload_claim() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE CONSTRAINT TRIGGER pipeline_run_input_upload_claim
    AFTER INSERT OR UPDATE ON pipeline_run_input_uploads DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION pipeline_run_input_upload_claim();
