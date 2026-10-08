-- Restores the shapes claims_only_reclaim dropped, LOSSILY.
--
-- Read leases, reclaim jobs and their attempts are not reconstructible: which
-- reads were in flight, which deletes were admitted and what the store
-- answered is gone, and inventing rows would be inventing that evidence. The
-- tables come back empty. A lifecycle gets its state back from reclaimed_at
-- (reclaimed_confirmed or registered), a metageneration of 1 (nothing
-- revalidates it) and a registered origin. A reader's expiring claim has no
-- spelling in the old shape, so it is released on the way down rather than
-- left as a consumer's hold nothing will ever give back.

-- The deferred constraint triggers a release fires (run_input_claim_lifetime,
-- run_candidate_claim_lifetime) run at the statement: a queued event would
-- otherwise stop the ALTER TABLE below in the same transaction (SQLSTATE
-- 55006), and no Run input or result names a reader's claim.
SET CONSTRAINTS ALL IMMEDIATE;
UPDATE hangar_claims SET released_at = coalesce(released_at, now()) WHERE expires_at IS NOT NULL;
ALTER TABLE hangar_claims DROP COLUMN expires_at;

DROP INDEX hangar_exact_lifecycles_reclaimable_idx;
ALTER TABLE hangar_exact_lifecycles
    ADD COLUMN metageneration bigint NOT NULL DEFAULT 1 CHECK (metageneration > 0),
    ADD COLUMN marker_version text NOT NULL DEFAULT 'hangar-output-v1' CHECK (marker_version = 'hangar-output-v1'),
    ADD COLUMN origin text NOT NULL DEFAULT 'registered' CHECK (origin IN ('registered', 'adopted')),
    ADD COLUMN state text NOT NULL DEFAULT 'registered'
        CHECK (state IN ('registered', 'adopted', 'reclaiming',
                         'reclaimed_confirmed', 'reclaimed_inferred',
                         'conflicted', 'missing_out_of_band')),
    ADD COLUMN lifetime_audited_at timestamp with time zone;
UPDATE hangar_exact_lifecycles SET state = 'reclaimed_confirmed', updated_at = reclaimed_at
    WHERE reclaimed_at IS NOT NULL;
ALTER TABLE hangar_exact_lifecycles
    ALTER COLUMN metageneration DROP DEFAULT,
    ALTER COLUMN marker_version DROP DEFAULT,
    ALTER COLUMN origin DROP DEFAULT,
    ALTER COLUMN state DROP DEFAULT,
    DROP COLUMN reclaimed_at;
CREATE INDEX hangar_exact_lifecycles_reclaimable_idx
    ON hangar_exact_lifecycles (registered_at)
    WHERE state IN ('registered', 'adopted');

CREATE FUNCTION hangar_lifecycle_transition() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.scope <> OLD.scope OR NEW.digest <> OLD.digest OR NEW.generation <> OLD.generation THEN
        RAISE EXCEPTION 'hangar: the exact ref of lifecycle % is immutable', OLD.id
            USING ERRCODE = 'JB001';
    END IF;
    IF OLD.state IN ('reclaimed_confirmed', 'reclaimed_inferred') AND NEW.state <> OLD.state THEN
        RAISE EXCEPTION 'hangar: lifecycle % is % and terminal; a reclaimed generation never resurrects',
            OLD.id, OLD.state
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.state IN ('reclaimed_confirmed', 'reclaimed_inferred') AND OLD.state <> 'reclaiming' THEN
        RAISE EXCEPTION 'hangar: lifecycle % finalized as % from %; admission marks a generation reclaiming durably before any external delete',
            OLD.id, NEW.state, OLD.state
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.state = 'reclaiming' AND OLD.state NOT IN ('registered', 'adopted') THEN
        RAISE EXCEPTION 'hangar: lifecycle % admitted to reclaim from %; only a registered or adopted generation may be',
            OLD.id, OLD.state
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_lifecycle_transition_guard
    BEFORE UPDATE ON hangar_exact_lifecycles
    FOR EACH ROW EXECUTE FUNCTION hangar_lifecycle_transition();

CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_exact_lifecycles
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW WHEN (NEW.origin = 'adopted')
    EXECUTE FUNCTION hangar_check_policy_admission();

CREATE TABLE hangar_read_leases (
    read_lease_id    uuid PRIMARY KEY,
    claim_id         uuid NOT NULL REFERENCES hangar_claims (claim_id) ON DELETE RESTRICT,
    lifecycle_id     bigint NOT NULL
        REFERENCES hangar_exact_lifecycles (id) ON DELETE RESTRICT,
    activation_epoch bigint NOT NULL,
    lease_fence      bigint NOT NULL CHECK (lease_fence > 0),
    granted_at       timestamp with time zone NOT NULL DEFAULT now(),
    renewed_at       timestamp with time zone NOT NULL DEFAULT now(),
    expires_at       timestamp with time zone NOT NULL,
    released_at      timestamp with time zone,
    grant_nonce        text NOT NULL CHECK (length(grant_nonce) BETWEEN 16 AND 128),
    destination_handle text NOT NULL CHECK (destination_handle <> ''),
    destination_volume text NOT NULL CHECK (destination_volume <> ''),
    stat_metageneration bigint NOT NULL CHECK (stat_metageneration > 0),
    stat_marker_version text NOT NULL CHECK (stat_marker_version <> ''),
    stat_observed_at    timestamp with time zone NOT NULL,
    lease_term_seconds integer NOT NULL
        CHECK (lease_term_seconds >= 900 AND lease_term_seconds <= 86400),
    CONSTRAINT hangar_read_lease_term CHECK (expires_at - granted_at >= interval '15 minutes')
);

CREATE FUNCTION hangar_read_lease_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'hangar: read lease % cannot be deleted; its tombstone is what prevents stale resurrection',
            OLD.read_lease_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lifecycle_id <> OLD.lifecycle_id OR NEW.claim_id <> OLD.claim_id THEN
        RAISE EXCEPTION 'hangar: read lease % was moved to another ref or claim', OLD.read_lease_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.grant_nonce <> OLD.grant_nonce
        OR NEW.destination_handle <> OLD.destination_handle
        OR NEW.destination_volume <> OLD.destination_volume THEN
        RAISE EXCEPTION 'hangar: read lease % changed what its grant binds; a re-mint is byte-identical or it is a different lease',
            OLD.read_lease_id
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lease_term_seconds <> OLD.lease_term_seconds THEN
        RAISE EXCEPTION 'hangar: read lease % changed its admitted term, % -> %; a renewal grants one term and does not choose a new one',
            OLD.read_lease_id, OLD.lease_term_seconds, NEW.lease_term_seconds
            USING ERRCODE = 'JB001';
    END IF;
    IF NEW.lease_fence < OLD.lease_fence THEN
        RAISE EXCEPTION 'hangar: read lease % moved its fence backwards, % -> %',
            OLD.read_lease_id, OLD.lease_fence, NEW.lease_fence
            USING ERRCODE = 'JB003';
    END IF;
    IF OLD.released_at IS NOT NULL AND NEW.released_at IS NULL THEN
        RAISE EXCEPTION 'hangar: read lease % was released at % and cannot reactivate',
            OLD.read_lease_id, OLD.released_at
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NEW;
END $$;

CREATE TRIGGER hangar_read_lease_guard_trigger
    BEFORE UPDATE OR DELETE ON hangar_read_leases
    FOR EACH ROW EXECUTE FUNCTION hangar_read_lease_guard();

CREATE TABLE hangar_reclaim_jobs (
    id                  bigserial PRIMARY KEY,
    lifecycle_id        bigint NOT NULL
        REFERENCES hangar_exact_lifecycles (id) ON DELETE RESTRICT,
    activation_epoch    bigint NOT NULL,
    owner_id            uuid NOT NULL,
    lease_fence         bigint NOT NULL CHECK (lease_fence > 0),
    generation          bigint NOT NULL CHECK (generation > 0),
    metageneration      bigint NOT NULL CHECK (metageneration > 0),
    admitted_at         timestamp with time zone NOT NULL DEFAULT now(),
    renewed_at          timestamp with time zone NOT NULL DEFAULT now(),
    expires_at          timestamp with time zone NOT NULL,
    absence_observed_at timestamp with time zone,
    outcome             text
        CHECK (outcome IN ('reclaimed_confirmed', 'reclaimed_inferred', 'conflicted', 'abandoned')),
    finalized_at        timestamp with time zone,
    CONSTRAINT hangar_reclaim_lease_term CHECK (expires_at - renewed_at >= interval '15 minutes'),
    CONSTRAINT hangar_reclaim_outcome_pairs CHECK ((outcome IS NULL) = (finalized_at IS NULL))
);

CREATE UNIQUE INDEX hangar_reclaim_jobs_one_active_idx
    ON hangar_reclaim_jobs (lifecycle_id) WHERE finalized_at IS NULL;

CREATE TABLE hangar_reclaim_attempts (
    id                 bigserial PRIMARY KEY,
    job_id             bigint NOT NULL REFERENCES hangar_reclaim_jobs (id) ON DELETE RESTRICT,
    lease_fence        bigint NOT NULL CHECK (lease_fence > 0),
    admitted_delete_at timestamp with time zone NOT NULL DEFAULT now(),
    outcome            text
        CHECK (outcome IN ('deleted', 'already_absent', 'generation_conflict',
                           'unauthorized', 'timeout', 'infrastructure_failure')),
    observed_at        timestamp with time zone,
    CONSTRAINT hangar_reclaim_attempt_pairs CHECK ((outcome IS NULL) = (observed_at IS NULL))
);

CREATE FUNCTION hangar_check_reclaim_exclusion() RETURNS trigger
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
    SELECT count(*) INTO leases FROM hangar_read_leases
        WHERE lifecycle_id = target AND released_at IS NULL AND expires_at > now();
    SELECT count(*) INTO pending FROM hangar_captures
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND state IN ('pending', 'publishing');
    SELECT pending + count(*) INTO pending FROM hangar_input_publications
        WHERE scope = lifecycle.scope AND digest = lifecycle.digest
          AND lifecycle_id IS NULL AND expires_at > clock_timestamp();

    IF reclaims > 0 AND (claims > 0 OR leases > 0 OR pending > 0) THEN
        RAISE EXCEPTION 'hangar: tree ref %/%/% has an admitted reclaim beside % active claim(s), % active read lease(s) and % unresolved capture(s) or publication(s); reclaim admission is refused while any of them exists',
            lifecycle.scope, lifecycle.digest, lifecycle.generation, claims, leases, pending
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_reclaim_exclusion
    AFTER INSERT OR UPDATE ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_exclusion();
CREATE CONSTRAINT TRIGGER hangar_reclaim_exclusion
    AFTER INSERT OR UPDATE ON hangar_claims
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_exclusion();
CREATE CONSTRAINT TRIGGER hangar_reclaim_exclusion
    AFTER INSERT OR UPDATE ON hangar_read_leases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_exclusion();

CREATE FUNCTION hangar_check_read_lease_claim() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    claim hangar_claims%ROWTYPE;
BEGIN
    SELECT * INTO claim FROM hangar_claims WHERE claim_id = NEW.claim_id;
    IF claim.lifecycle_id <> NEW.lifecycle_id THEN
        RAISE EXCEPTION 'hangar: read lease % reads lifecycle % under a claim on lifecycle %',
            NEW.read_lease_id, NEW.lifecycle_id, claim.lifecycle_id
            USING ERRCODE = 'JB001';
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_read_lease_matches_claim
    AFTER INSERT OR UPDATE ON hangar_read_leases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_read_lease_claim();

CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();
CREATE CONSTRAINT TRIGGER hangar_policy_admits_new_protection
    AFTER INSERT ON hangar_read_leases
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_policy_admission();

CREATE FUNCTION hangar_check_reclaim_admission() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_integrity_findings
               WHERE resolved_at IS NULL AND violation = 'out_of_band_absence') THEN
        RAISE EXCEPTION 'hangar: unexplained object loss; reclamation requires explicit reconciliation'
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_reclaim_admission_needs_reconciliation
    AFTER INSERT ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_admission();

CREATE FUNCTION hangar_check_reclaim_evidence() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    confirmed integer;
    absent_at bigint;
    lost      integer;
BEGIN
    IF NEW.outcome IS NULL THEN
        RETURN NULL;
    END IF;

    SELECT count(*) INTO confirmed FROM hangar_reclaim_attempts
        WHERE job_id = NEW.id AND outcome = 'deleted';

    IF NEW.outcome = 'reclaimed_confirmed' AND confirmed = 0 THEN
        RAISE EXCEPTION 'hangar: reclaim job % finalized as reclaimed_confirmed with no acknowledged conditional delete; an ambiguous deletion is inferred at best',
            NEW.id
            USING ERRCODE = 'JB004';
    END IF;
    IF NEW.outcome = 'reclaimed_inferred' THEN
        SELECT min(id) INTO absent_at FROM hangar_reclaim_attempts
            WHERE job_id = NEW.id AND outcome = 'already_absent';

        SELECT count(*) INTO lost FROM hangar_reclaim_attempts
            WHERE job_id = NEW.id
              AND (outcome IS NULL OR outcome IN ('timeout', 'infrastructure_failure'))
              AND (absent_at IS NULL OR id < absent_at);

        IF lost = 0 THEN
            RAISE EXCEPTION 'hangar: reclaim job % finalized as reclaimed_inferred with no earlier admitted delete whose response was lost; every attempt on this job was answered, so nothing this plane did can explain the absence, and an absence this plane cannot explain is an out-of-band lifetime violation rather than normal reclamation',
                NEW.id
            USING ERRCODE = 'JB004';
        END IF;
        IF NEW.absence_observed_at IS NULL THEN
            RAISE EXCEPTION 'hangar: reclaim job % finalized as reclaimed_inferred without observing exact absence',
                NEW.id
            USING ERRCODE = 'JB004';
        END IF;
    END IF;

    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER hangar_reclaim_evidence
    AFTER INSERT OR UPDATE ON hangar_reclaim_jobs
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION hangar_check_reclaim_evidence();

CREATE INDEX hangar_read_leases_active_idx
    ON hangar_read_leases (lifecycle_id) WHERE released_at IS NULL;
CREATE INDEX hangar_read_leases_expiry_idx
    ON hangar_read_leases (expires_at) WHERE released_at IS NULL;
