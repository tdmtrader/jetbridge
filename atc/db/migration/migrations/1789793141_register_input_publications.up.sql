-- Input bytes have no producing execution. Their pre-publication correlation
-- and signed exact observation are separate from capture/quiescence evidence.
CREATE TABLE hangar_input_publications (
    reservation_id uuid PRIMARY KEY,
    scope text NOT NULL CHECK (scope ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    digest text NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    activation_epoch bigint NOT NULL REFERENCES hangar_output_activation_epochs(epoch_id) ON DELETE RESTRICT,
    stage jsonb NOT NULL CHECK (jsonb_typeof(stage) = 'object' AND octet_length(stage::text) <= 4096),
    nonce uuid NOT NULL UNIQUE CHECK (nonce <> '00000000-0000-0000-0000-000000000000'),
    expires_at timestamptz NOT NULL,
    lifecycle_id bigint REFERENCES hangar_exact_lifecycles(id) ON DELETE RESTRICT,
    publication jsonb CHECK (jsonb_typeof(publication) = 'object' AND octet_length(publication::text) <= 16384),
    registered_at timestamptz,
    CHECK ((lifecycle_id IS NULL AND publication IS NULL AND registered_at IS NULL)
        OR (lifecycle_id IS NOT NULL AND publication IS NOT NULL AND registered_at IS NOT NULL))
);
CREATE INDEX hangar_pending_input_correlation ON hangar_input_publications(scope, digest, reservation_id)
    WHERE lifecycle_id IS NULL;

CREATE FUNCTION hangar_input_publication_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER hangar_input_publication_immutable BEFORE UPDATE OR DELETE ON hangar_input_publications
    FOR EACH ROW EXECUTE FUNCTION hangar_input_publication_immutable();

CREATE FUNCTION hangar_check_input_publication() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE CONSTRAINT TRIGGER hangar_input_publication_matches
    AFTER INSERT OR UPDATE ON hangar_input_publications DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_input_publication();
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_input_publications DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();

CREATE OR REPLACE FUNCTION hangar_check_reclaim_exclusion() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    target    bigint;
    lifecycle hangar_exact_lifecycles%ROWTYPE;
    reclaims  integer;
    claims    integer;
    leases    integer;
    pending   integer;
BEGIN
    target := NEW.lifecycle_id;
    SELECT * INTO lifecycle FROM hangar_exact_lifecycles WHERE id = target;

    SELECT count(*) INTO reclaims FROM hangar_reclaim_jobs
        WHERE lifecycle_id = target AND finalized_at IS NULL;
    SELECT count(*) INTO claims FROM hangar_claims
        WHERE lifecycle_id = target AND released_at IS NULL;
    -- An EXPIRED lease is not an active one. AC 13 says a reader's protection
    -- ends when the read lease closes OR safely expires, and counting an
    -- abandoned lease forever would make one crashed materializer pin a
    -- generation for the life of the deployment. Recovery still closes it --
    -- only after this same database clock says it has expired -- and the
    -- tombstone is what stops it resurrecting.
    SELECT count(*) INTO leases FROM hangar_read_leases
        WHERE lifecycle_id = target AND released_at IS NULL AND expires_at > now();
    SELECT count(*) INTO pending FROM hangar_logical_reservations
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND state = 'unresolved_generation';
    SELECT pending + count(*) INTO pending FROM hangar_input_publications
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND lifecycle_id IS NULL AND expires_at > clock_timestamp();

    IF reclaims > 0 AND (claims > 0 OR leases > 0 OR pending > 0) THEN
        RAISE EXCEPTION 'hangar: tree ref %/%/% has an admitted reclaim beside % active claim(s), % active read lease(s) and % unresolved reservation(s); reclaim admission is refused while any of them exists',
            lifecycle.scope, lifecycle.digest, lifecycle.generation, claims, leases, pending
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;
