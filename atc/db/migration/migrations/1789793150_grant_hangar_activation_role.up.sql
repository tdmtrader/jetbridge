-- The activation database role: the PostgreSQL role the activation, drain and
-- reconcile-integrity commands run as (hangar_activation_db_role). Its name is
-- per database, so two installs on one server never share authority.
CREATE OR REPLACE FUNCTION hangar_activation_role_name() RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT current_database() || '_hangar_activation'
$$;

-- hangar_activation_grants gives the role exactly the operations those
-- commands perform, and takes back anything else it was given. It is called
-- here when the role exists, and by the bootstrap's database step right after
-- it creates the role, so a GRANT never names a role that is not there yet.
-- A migration that adds or changes a table the commands write extends it.
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

    -- begin inserts the epoch; attest, enable and drain move its facets.
    EXECUTE format('GRANT SELECT, INSERT ON hangar_output_activation_epochs TO %I', role_name);
    EXECUTE format('GRANT UPDATE (base_state, base_attestation, protocol_version, ledger_version, '
        'cohort_digest, output_state, output_attestation, receipt_public_key_id, '
        'receipt_key_valid_from, receipt_key_valid_until, materialization_key_id, '
        'bucket_fingerprint, derived_namespace, revision, updated_at) '
        'ON hangar_output_activation_epochs TO %I', role_name);

    -- enable checks the schema and the open findings; drain reads the residue.
    FOREACH readable IN ARRAY ARRAY[
        'migrations_history',
        'hangar_handoff_predeclarations', 'hangar_handoff_dispositions',
        'hangar_capture_reservations', 'hangar_exact_lifecycles', 'hangar_claims',
        'hangar_read_leases', 'hangar_reclaim_jobs', 'hangar_inventory_debt'
    ] LOOP
        EXECUTE format('GRANT SELECT ON %I TO %I', readable, role_name);
    END LOOP;

    -- reconcile-integrity closes a finding and keeps its history.
    EXECUTE format('GRANT SELECT ON hangar_policy_violations TO %I', role_name);
    EXECUTE format('GRANT UPDATE (resolved_at) ON hangar_policy_violations TO %I', role_name);
END $$;

SELECT hangar_activation_grants();

-- Web's role keeps UPDATE on epoch_id alone: its FOR SHARE lock on the row and
-- the foreign keys referencing it need UPDATE on some column. Every write,
-- epoch_id included, is refused by the guard trigger unless the writer is the
-- activation database role (hangar_activation_db_role B1). A superuser keeps
-- every privilege regardless; there only the trigger holds.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON hangar_output_activation_epochs FROM CURRENT_USER;
GRANT UPDATE (epoch_id) ON hangar_output_activation_epochs TO CURRENT_USER;
