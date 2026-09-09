-- The down migration refuses before it touches anything.
--
-- Req 58 says claim, read-lease, lifecycle, policy, inventory and reclaimer
-- state must stay compatible while any managed publication, claim or lease
-- exists, and that unsafe removal is blocked rather than promised after a
-- finite drain. A DROP TABLE cannot be blocked after the fact, so the refusal
-- is the first statement in the file: this runs inside the migrator's single
-- transaction, so a RAISE here aborts before any DDL is attempted and the
-- schema is exactly as it was.
--
-- What it permits is the honest inverse of what the up migration created: a
-- held schema, no epoch that ever left `initial`, and no row anywhere.
DO $$
DECLARE
    blockers text[] := ARRAY[]::text[];
    n        bigint;
BEGIN
    SELECT count(*) INTO n FROM hangar_output_activation_epochs
        WHERE base_state <> 'initial' OR output_state <> 'initial';
    IF n > 0 THEN
        blockers := blockers || format('%s activation epoch(s) past initial', n);
    END IF;

    SELECT count(*) INTO n FROM hangar_handoff_predeclarations;
    IF n > 0 THEN blockers := blockers || format('%s handoff predeclaration(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_capture_reservations;
    IF n > 0 THEN blockers := blockers || format('%s capture reservation(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_logical_reservations;
    IF n > 0 THEN blockers := blockers || format('%s logical reservation(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_output_receipts;
    IF n > 0 THEN blockers := blockers || format('%s registered receipt(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_exact_lifecycles;
    IF n > 0 THEN blockers := blockers || format('%s exact lifecycle record(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_claims;
    IF n > 0 THEN blockers := blockers || format('%s claim(s) or claim tombstone(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_read_leases;
    IF n > 0 THEN blockers := blockers || format('%s read lease(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_inventory_cursors;
    IF n > 0 THEN blockers := blockers || format('%s inventory cursor(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_inventory_debt;
    IF n > 0 THEN blockers := blockers || format('%s inventory debt record(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_reclaim_jobs;
    IF n > 0 THEN blockers := blockers || format('%s reclaim job(s)', n); END IF;
    SELECT count(*) INTO n FROM hangar_policy_snapshots;
    IF n > 0 THEN blockers := blockers || format('%s policy snapshot(s)', n); END IF;

    IF array_length(blockers, 1) IS NOT NULL THEN
        RAISE EXCEPTION 'hangar: refusing to remove the output plane while it holds state: %. Drain output first: the claim, read-lease, lifecycle, inventory, reclaim and policy surfaces stay compatible while any managed publication, claim or lease exists, and removing them is not something a finite wait makes safe.',
            array_to_string(blockers, ', ');
    END IF;
END $$;

DROP TABLE hangar_operation_leases;
DROP TABLE hangar_policy_snapshots;
DROP TABLE hangar_reclaim_attempts;
DROP TABLE hangar_reclaim_jobs;
DROP TABLE hangar_inventory_debt;
DROP TABLE hangar_inventory_cursors;
DROP TABLE hangar_read_leases;
DROP TABLE hangar_claims;
DROP TABLE hangar_output_receipts;
DROP TABLE hangar_receipt_stat_challenges;
DROP TABLE hangar_exact_lifecycles;
DROP TABLE hangar_logical_reservations;
DROP TABLE hangar_pre_reservation_cancel_dispositions;
DROP TABLE hangar_no_capture_dispositions;
DROP TABLE hangar_capture_attempt_leases;
DROP TABLE hangar_capture_reservations;
DROP TABLE hangar_handoff_dispositions;
DROP TABLE hangar_handoff_predeclarations;
DROP TABLE hangar_output_activation_epochs;

DROP FUNCTION hangar_check_reclaim_evidence();
DROP FUNCTION hangar_check_policy_admission();
DROP FUNCTION hangar_check_read_lease_claim();
DROP FUNCTION hangar_check_reclaim_exclusion();
DROP FUNCTION hangar_check_receipt_admission();
DROP FUNCTION hangar_check_create_follows_resolution();
DROP FUNCTION hangar_check_logical_resolution();
DROP FUNCTION hangar_check_stage_two();
DROP FUNCTION hangar_check_handoff_branch();
DROP FUNCTION hangar_operation_lease_fence();
DROP FUNCTION hangar_cursor_fence();
DROP FUNCTION hangar_read_lease_guard();
DROP FUNCTION hangar_claim_tombstone();
DROP FUNCTION hangar_challenge_one_use();
DROP FUNCTION hangar_lifecycle_transition();
DROP FUNCTION hangar_logical_reservation_immutable();
DROP FUNCTION hangar_capture_release_is_one_way();
DROP FUNCTION hangar_capture_lease_fence();
DROP FUNCTION hangar_disposition_immutable();
DROP FUNCTION hangar_predeclaration_immutable();
DROP FUNCTION hangar_output_epoch_transition();
DROP FUNCTION hangar_output_facet_ordinal(text);
