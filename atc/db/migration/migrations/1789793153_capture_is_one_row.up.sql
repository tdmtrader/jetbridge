-- capture_is_one_row, part two: the capture row becomes the only capture
-- state, and everything it replaces goes.
--
-- Admission still consults the activation epoch in this track (hangar_enabled
-- is created and seeded by 1789793152 and switched on by no_activation).

-- A Run still producing through the old handoff tables cannot be carried
-- across: its node-side state is in a ledger format the new daemon does not
-- read. Refuse rather than strand it; drain (let running Runs finish or cancel
-- them) and migrate again.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_output_starts s
               JOIN pipeline_runs r ON r.id = s.run_id
               WHERE r.status = 'running') THEN
        RAISE EXCEPTION 'capture_is_one_row: a running Run still owns an output capture under the handoff tables; let it finish or cancel it, then migrate again';
    END IF;
END $$;

-- Completed Runs keep their history: each producer becomes a terminal,
-- released capture row (published when the Run retained a candidate for it,
-- discarded otherwise), and the Run's side becomes pipeline_run_captures.
INSERT INTO hangar_captures
    (execution_id, execution_fence, output_name, state, node, node_uid,
     scope, digest, generation, capture_deadline_at, released_at, error, created_at, finished_at)
SELECT h.execution_id, h.execution_fence, h.output_name,
       CASE WHEN c.handoff_id IS NOT NULL THEN 'published' ELSE 'discarded' END,
       s.node_name, s.node_uid,
       c.scope, c.digest, c.generation,
       h.capture_deadline_at, now(),
       CASE WHEN c.handoff_id IS NOT NULL THEN NULL ELSE 'migrated from a settled handoff' END,
       h.created_at, now()
FROM pipeline_run_output_starts s
JOIN hangar_handoff_predeclarations h USING (handoff_id)
LEFT JOIN pipeline_run_output_candidates c USING (handoff_id);

INSERT INTO pipeline_run_captures
    (run_id, build_id, task_id, result_name, task_name, execution_id, output_name)
SELECT s.run_id, s.build_id, s.task_id, s.result_name, s.task_name, h.execution_id, h.output_name
FROM pipeline_run_output_starts s
JOIN hangar_handoff_predeclarations h USING (handoff_id);

-- The execution that captured names its capture by output, not by handoff.
ALTER TABLE pipeline_run_executions DISABLE TRIGGER run_execution_identity_immutable;
UPDATE pipeline_run_executions e SET capture_output = h.output_name
FROM hangar_handoff_predeclarations h
WHERE e.handoff_id = h.handoff_id;
ALTER TABLE pipeline_run_executions ENABLE TRIGGER run_execution_identity_immutable;
ALTER TABLE pipeline_run_executions DROP COLUMN handoff_id;

-- The credential handoff names the capture whose producer receives it.
ALTER TABLE pipeline_run_credential_handoffs DISABLE TRIGGER immutable_run_credential_handoff;
ALTER TABLE pipeline_run_credential_handoffs ADD COLUMN execution_id uuid, ADD COLUMN output_name text;
UPDATE pipeline_run_credential_handoffs c SET execution_id = h.execution_id, output_name = h.output_name
FROM hangar_handoff_predeclarations h
WHERE c.handoff_id = h.handoff_id;
ALTER TABLE pipeline_run_credential_handoffs DROP COLUMN handoff_id;
ALTER TABLE pipeline_run_credential_handoffs ALTER COLUMN execution_id SET NOT NULL,
    ALTER COLUMN output_name SET NOT NULL,
    ADD CONSTRAINT pipeline_run_credential_handoffs_capture_key UNIQUE (execution_id, output_name),
    ADD CONSTRAINT pipeline_run_credential_handoffs_capture_fkey
        FOREIGN KEY (execution_id, output_name) REFERENCES hangar_captures (execution_id, output_name)
        ON DELETE RESTRICT;
ALTER TABLE pipeline_run_credential_handoffs ENABLE TRIGGER immutable_run_credential_handoff;

CREATE OR REPLACE FUNCTION immutable_run_credential_handoff() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        RAISE EXCEPTION 'A Run credential handoff cannot be reseeded';
    END IF;
    IF TG_OP = 'UPDATE' AND (
        (NEW.run_id, NEW.execution_id, NEW.output_name, NEW.claimed_at)
            IS DISTINCT FROM (OLD.run_id, OLD.execution_id, OLD.output_name, OLD.claimed_at)
        OR (OLD.ready_at IS NOT NULL AND NEW.ready_at IS DISTINCT FROM OLD.ready_at)
    ) THEN
        RAISE EXCEPTION 'Run credential delivery identity and readiness are immutable';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_captures s JOIN pipeline_run_invocations i ON i.run_id = s.run_id
        WHERE s.execution_id = NEW.execution_id AND s.output_name = NEW.output_name AND s.run_id = NEW.run_id) THEN
        RAISE EXCEPTION 'A credential handoff must belong to its invoked Run';
    END IF;
    RETURN NEW;
END $$;

-- Integrity findings, without an epoch. Open duplicates across epochs
-- collapse to the oldest.
INSERT INTO hangar_integrity_findings (violation, subject, detail, observed_at, resolved_at)
SELECT violation, subject, detail, observed_at, resolved_at
FROM hangar_policy_violations WHERE resolved_at IS NOT NULL ORDER BY id;
INSERT INTO hangar_integrity_findings (violation, subject, detail, observed_at)
SELECT DISTINCT ON (violation, subject) violation, subject, detail, observed_at
FROM hangar_policy_violations WHERE resolved_at IS NULL ORDER BY violation, subject, id;

CREATE OR REPLACE FUNCTION hangar_check_policy_admission() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_integrity_findings
               WHERE resolved_at IS NULL
                 AND violation IN ('out_of_band_absence', 'runtime_principal_denied')) THEN
        RAISE EXCEPTION 'hangar: unresolved storage integrity findings; repair the cause and explicitly reconcile each finding before admitting new work'
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION hangar_check_reclaim_admission() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM hangar_integrity_findings
               WHERE resolved_at IS NULL AND violation = 'out_of_band_absence') THEN
        RAISE EXCEPTION 'hangar: unexplained object loss; reclamation requires explicit reconciliation'
            USING ERRCODE = 'JB002';
    END IF;
    RETURN NULL;
END $$;

-- Reclaim admission: a pending or publishing capture of the tree, an open
-- claim, a live read lease or an unregistered input publication excludes it.
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

-- A Run's result claims are its published captures' claims.
CREATE OR REPLACE FUNCTION check_run_candidate_claim_lifetime() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE owner bigint;
BEGIN
 SELECT s.run_id INTO owner FROM pipeline_run_captures s
  WHERE NEW.consumer_binding_id = 'capture:' || s.execution_id::text || '/' || s.output_name;
 IF owner IS NOT NULL THEN PERFORM check_run_result_claims(owner); END IF;
 RETURN NULL;
END;
$$;

CREATE OR REPLACE FUNCTION check_run_result_claims(checked_run bigint) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE r pipeline_runs%ROWTYPE;
BEGIN
 SELECT * INTO r FROM pipeline_runs WHERE id=checked_run;
 IF r.id IS NULL THEN RETURN; END IF;
 IF EXISTS (
  SELECT 1 FROM pipeline_run_captures s
  JOIN hangar_captures c USING (execution_id, output_name)
  JOIN hangar_claims claim ON claim.consumer_binding_id = 'capture:' || s.execution_id::text || '/' || s.output_name
  WHERE s.run_id=r.id AND c.state='published' AND ((claim.released_at IS NULL) IS DISTINCT FROM
   (r.status='running' OR EXISTS(SELECT 1 FROM jsonb_each(r.result_manifest) item
     WHERE item.value->>'claim_id'=claim.claim_id::text)))
 ) THEN RAISE EXCEPTION 'Run publication and candidate claim lifetime must commit together'; END IF;
 IF r.status='running' THEN RETURN; END IF;
 IF EXISTS(SELECT 1 FROM pipeline_run_captures s JOIN hangar_captures c USING (execution_id, output_name)
           WHERE s.run_id=r.id AND c.state IN ('pending','publishing')) OR
    EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id=r.id AND status IN ('pending','started')) THEN
  RAISE EXCEPTION 'Run terminal publication requires closed producer and build work';
 END IF;
 IF EXISTS (
  SELECT 1 FROM jsonb_each(r.result_manifest) item WHERE NOT EXISTS (
   SELECT 1 FROM pipeline_run_captures s
   JOIN hangar_captures c USING (execution_id, output_name)
   JOIN hangar_claims claim ON claim.consumer_binding_id = 'capture:' || s.execution_id::text || '/' || s.output_name
   WHERE s.run_id=r.id AND s.result_name=item.key AND c.state='published' AND item.value=jsonb_build_object(
    'claim_id',claim.claim_id::text,'ref',jsonb_build_object('scope',c.scope,'digest',c.digest,'generation',c.generation))
  )
 ) THEN RAISE EXCEPTION 'Run manifest requires its exact retained candidates'; END IF;
END;
$$;

-- An execution is closed by its own closure; a capture never closes one.
CREATE OR REPLACE FUNCTION run_execution_closed(build bigint) RETURNS boolean
LANGUAGE sql STABLE AS $$
 SELECT NOT EXISTS(SELECT 1 FROM pipeline_run_executions e
 LEFT JOIN pipeline_run_execution_closures c USING(execution_id,execution_fence)
 WHERE e.build_id=build AND c.execution_id IS NULL);
$$;

CREATE OR REPLACE FUNCTION immutable_run_output_evidence() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF run_team_purge_active() THEN
            RETURN OLD;
        END IF;
        IF run_check_gc_active() THEN
            -- Nested: OLD's columns differ by table, so each test is reached
            -- only for the table that has them.
            IF TG_TABLE_NAME = 'pipeline_run_executions' THEN
                IF OLD.kind IN ('check', 'get') AND OLD.capture_output IS NULL
                   AND EXISTS (SELECT 1 FROM pipeline_run_execution_closures c
                       WHERE c.execution_id = OLD.execution_id AND c.execution_fence = OLD.execution_fence)
                   AND EXISTS (SELECT 1 FROM builds b WHERE b.id = OLD.build_id AND b.completed
                       AND b.pipeline_run_id IS NOT NULL AND b.run_job_name IS NULL)
                   AND NOT EXISTS (SELECT 1 FROM pipeline_run_captures s WHERE s.build_id = OLD.build_id) THEN
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

-- The activation role reads the capture rows and closes findings.
CREATE OR REPLACE FUNCTION hangar_activation_grants() RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    role_name text := hangar_activation_role_name();
    readable  text;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = role_name) THEN
        RETURN;
    END IF;
    EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', role_name);
    EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', role_name);

    EXECUTE format('GRANT SELECT, INSERT ON hangar_output_activation_epochs TO %I', role_name);
    EXECUTE format('GRANT UPDATE (base_state, base_attestation, protocol_version, ledger_version, '
        'cohort_digest, output_state, output_attestation, receipt_public_key_id, '
        'receipt_key_valid_from, receipt_key_valid_until, materialization_key_id, '
        'bucket_fingerprint, derived_namespace, revision, updated_at) '
        'ON hangar_output_activation_epochs TO %I', role_name);

    FOREACH readable IN ARRAY ARRAY[
        'migrations_history', 'hangar_captures', 'hangar_exact_lifecycles', 'hangar_claims',
        'hangar_read_leases', 'hangar_reclaim_jobs', 'hangar_inventory_debt'
    ] LOOP
        EXECUTE format('GRANT SELECT ON %I TO %I', readable, role_name);
    END LOOP;

    EXECUTE format('GRANT SELECT ON hangar_integrity_findings TO %I', role_name);
    EXECUTE format('GRANT UPDATE (resolved_at) ON hangar_integrity_findings TO %I', role_name);
END $$;
SELECT hangar_activation_grants();

-- An enabled epoch no longer names a receipt key: there are no receipts.
ALTER TABLE hangar_output_activation_epochs
    DROP CONSTRAINT hangar_output_epoch_enabled_output_identity,
    ADD CONSTRAINT hangar_output_epoch_enabled_output_identity CHECK (output_state <> 'enabled' OR
        (materialization_key_id IS NOT NULL AND bucket_fingerprint IS NOT NULL AND derived_namespace IS NOT NULL));

-- The handoff, its dispositions, reservations, receipts, challenges, capture
-- leases, policy snapshots and announcements, and every Run-side table that
-- recorded one.
DROP TABLE pipeline_run_output_cancellation_classifications;
DROP TABLE pipeline_run_output_cancellation_evidence;
DROP TABLE pipeline_run_output_candidates;
DROP TABLE pipeline_run_output_discards;
DROP TABLE pipeline_run_output_releases;
DROP TABLE pipeline_run_output_holds;
DROP TABLE pipeline_run_output_finishes;
DROP TABLE pipeline_run_output_starts;
DROP TABLE hangar_output_receipts;
DROP TABLE hangar_receipt_stat_challenges;
DROP TABLE hangar_logical_reservations;
DROP TABLE hangar_capture_attempt_leases;
DROP TABLE hangar_capture_reservations;
DROP TABLE hangar_no_capture_dispositions;
DROP TABLE hangar_pre_reservation_cancel_dispositions;
DROP TABLE hangar_handoff_dispositions;
DROP TABLE hangar_capture_announcements;
DROP TABLE hangar_handoff_predeclarations;
DROP TABLE hangar_policy_violations;
DROP TABLE hangar_policy_snapshots;

DROP FUNCTION check_run_cancel_classified();
DROP FUNCTION check_run_cancel_source_proof();
DROP FUNCTION check_run_output_candidate();
DROP FUNCTION check_run_output_disposition();
DROP FUNCTION check_run_output_release();
DROP FUNCTION immutable_run_output_start();
DROP FUNCTION run_output_closed(uuid);
DROP FUNCTION hangar_capture_lease_fence();
DROP FUNCTION hangar_capture_release_is_one_way();
DROP FUNCTION hangar_capture_state_is_terminal();
DROP FUNCTION hangar_challenge_one_use();
DROP FUNCTION hangar_check_create_follows_resolution();
DROP FUNCTION hangar_check_handoff_branch();
DROP FUNCTION hangar_check_logical_resolution();
DROP FUNCTION hangar_check_receipt_admission();
DROP FUNCTION hangar_check_stage_two();
DROP FUNCTION hangar_disposition_immutable();
DROP FUNCTION hangar_logical_reservation_immutable();
DROP FUNCTION hangar_predeclaration_immutable();
DROP FUNCTION hangar_policy_violation_resolution();
