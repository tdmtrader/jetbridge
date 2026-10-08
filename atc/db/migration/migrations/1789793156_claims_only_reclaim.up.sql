-- claims_only_reclaim: one refcount and one reclaim action.
--
-- A claim is the only hold on a generation. A consumer's hold (a Run's result
-- binding, an input publication, a capture's own) has no expiry; a reader's
-- hold expires, so an abandoned read pins nothing for the life of the
-- deployment. A read lease, the second refcount a reader used to take beside
-- the consumer's claim, goes with its table and its guard.
--
-- Reclamation is one pass: a registered generation that no live claim, no
-- pending or publishing capture and no unregistered input publication names
-- is deleted by its exact generation and stamped reclaimed, in one
-- transaction under the tree lock. The reclaim job, its fenced lease, its
-- attempts, its four-way outcome and the evidence trigger that adjudicated
-- them go: there is no admitted-but-not-finalized state to record, because
-- the stamp follows the store's answer in the same transaction.
--
-- A lifecycle is registered or reclaimed. Its metageneration (a stat proof
-- nothing revalidates any more), marker version (one admitted value), origin
-- (adoption is gone), lifetime audit stamp (no audit) and seven-way state go;
-- reclaimed_at replaces the state. A reclaimed generation never resurrects:
-- registration is INSERT ... ON CONFLICT DO NOTHING and the registering
-- transaction refuses a row that is already reclaimed.

-- The lifecycle's state, folded into one stamp. Confirmed and inferred
-- reclamations are reclaimed. Conflicted is reclaimed too: the exact
-- generation is gone and the object at the key is someone else's. A
-- generation caught mid-reclaim (`reclaiming`) is registered again and the
-- next pass retries its delete; one recorded missing out of band stays
-- registered beside its open finding, and the next pass finds it absent.
--
-- The old transition guard goes first: it refuses any UPDATE of a row already
-- reclaimed, and the stamp below is exactly that.
DROP TRIGGER hangar_lifecycle_transition_guard ON hangar_exact_lifecycles;
DROP FUNCTION hangar_lifecycle_transition();
ALTER TABLE hangar_exact_lifecycles ADD COLUMN reclaimed_at timestamptz;
UPDATE hangar_exact_lifecycles SET reclaimed_at = updated_at
    WHERE state IN ('reclaimed_confirmed', 'reclaimed_inferred', 'conflicted');

-- The admission gate on adopted lifecycles: adoption is gone, and the gate's
-- WHEN clause names the column dropped below.
DROP TRIGGER hangar_policy_admits_new_protection ON hangar_exact_lifecycles;
DROP INDEX hangar_exact_lifecycles_reclaimable_idx;
ALTER TABLE hangar_exact_lifecycles
    DROP COLUMN metageneration,
    DROP COLUMN marker_version,
    DROP COLUMN origin,
    DROP COLUMN lifetime_audited_at,
    DROP COLUMN state;
CREATE INDEX hangar_exact_lifecycles_reclaimable_idx
    ON hangar_exact_lifecycles (registered_at) WHERE reclaimed_at IS NULL;

-- A reader's hold expires; a consumer's does not. The tombstone semantics are
-- unchanged: released_at is one-way and the row is never deleted.
ALTER TABLE hangar_claims ADD COLUMN expires_at timestamptz;

-- The read lease, the reclaim job and its attempts, and every function that
-- existed only for them. hangar_claim_tombstone and
-- hangar_check_policy_admission stay; their triggers on the dropped tables go
-- with the tables.
DROP TRIGGER hangar_reclaim_exclusion ON hangar_claims;
DROP TABLE hangar_reclaim_attempts;
DROP TABLE hangar_reclaim_jobs;
DROP TABLE hangar_read_leases;
DROP FUNCTION hangar_check_reclaim_exclusion();
DROP FUNCTION hangar_check_reclaim_admission();
DROP FUNCTION hangar_check_reclaim_evidence();
DROP FUNCTION hangar_check_read_lease_claim();
DROP FUNCTION hangar_read_lease_guard();
