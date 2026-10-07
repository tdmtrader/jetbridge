CREATE OR REPLACE FUNCTION public.hangar_check_reclaim_exclusion()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
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
END $function$

;
ALTER TABLE pipeline_run_executions DROP CONSTRAINT pipeline_run_executions_capture_fkey;
ALTER TABLE pipeline_run_executions DROP COLUMN capture_output;
DROP TABLE pipeline_run_captures;
DROP TABLE hangar_integrity_findings;
DROP FUNCTION hangar_integrity_finding_resolution();
DROP TABLE hangar_enabled;
DROP TABLE hangar_captures;
DROP FUNCTION hangar_capture_transition();
