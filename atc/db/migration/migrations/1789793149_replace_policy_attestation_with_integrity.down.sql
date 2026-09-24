-- Restores the previous admission contract; a fresh attestation is required again.
CREATE OR REPLACE FUNCTION hangar_check_policy_admission() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    latest hangar_policy_snapshots%ROWTYPE;
BEGIN
    SELECT * INTO latest FROM hangar_policy_snapshots
        WHERE activation_epoch = NEW.activation_epoch
        ORDER BY observed_at DESC, id DESC LIMIT 1;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'hangar: epoch % has no lifetime-policy attestation; "we have not checked" and "the check failed" are the same amount of evidence',
            NEW.activation_epoch
            USING ERRCODE = 'JB002';
    END IF;
    IF latest.state <> 'safe' THEN
        RAISE EXCEPTION 'hangar: epoch % is % on its lifetime policy; new captures, claim acquires, grants, adoption and reclaim admission stop from detection onward',
            NEW.activation_epoch, latest.state
            USING ERRCODE = 'JB002';
    END IF;
    -- now() is transaction_timestamp(), so a transaction open for N seconds
    -- measures this bound N seconds early and can admit an attestation older
    -- than fifteen minutes by its own duration. Recorded and left as now() on
    -- purpose: this trigger is DEFERRED, so it fires in the constraint phase of
    -- a transaction whose other now() readings -- the claim's requested_at, the
    -- lease instants it was admitted under -- are all transaction start, and a
    -- freshness bound read from a later clock than the row it is admitting
    -- would be the mixture of two moments this schema exists to refuse. The
    -- error is bounded by transaction duration, the bound is fifteen minutes,
    -- and the admission transactions here are milliseconds long. The one place
    -- the distinction is load-bearing is the seal deadline, which is
    -- configurable down to thirty seconds and reads clock_timestamp(); see
    -- SealDeadlinePassed.
    IF now() - latest.observed_at > interval '15 minutes' THEN
        RAISE EXCEPTION 'hangar: the lifetime-policy attestation for epoch % is % old, past the 15-minute detection bound; a stale check is not a safe one',
            NEW.activation_epoch, now() - latest.observed_at
            USING ERRCODE = 'JB002';
    END IF;

    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION hangar_check_reclaim_admission() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    unreconciled integer;
BEGIN
    SELECT count(*) INTO unreconciled FROM hangar_policy_violations
        WHERE activation_epoch = NEW.activation_epoch
          AND resolved_at IS NULL
          AND violation IN ('lifecycle_delete_rule', 'out_of_band_absence');

    IF unreconciled > 0 THEN
        RAISE EXCEPTION 'hangar: epoch % has % unreconciled lifetime violation(s) saying something other than this plane may be removing its objects; a fresh safe attestation reopens the rest of the plane, and reclaim admission waits for reconciliation, because resuming deletion beside an unexplained deleter is how this plane finishes a job something else started',
            NEW.activation_epoch, unreconciled
            USING ERRCODE = 'JB002';
    END IF;

    RETURN NULL;
END $$;
