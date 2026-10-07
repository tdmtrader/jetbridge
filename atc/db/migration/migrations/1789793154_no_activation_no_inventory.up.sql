-- no_activation_no_inventory: the activation epoch, its database role, the
-- operation leases and the inventory sweep's cursor and debt go.
--
-- In service is now one row: hangar_enabled (created and seeded by
-- 1789793152), taken FOR SHARE by every admission. The web's
-- hangarOutput.webEnabled flag seeds and moves it at startup.
--
-- Rows elsewhere keep their activation_epoch column as a plain recorded number
-- (the control-key generation a lifecycle, claim, lease or input was admitted
-- under); only its reference to an epoch row goes, because there are no epoch
-- rows any more. Nothing moves an epoch, and the output scope no longer derives
-- from one. The number still matters where a row is compared with the
-- configured epoch: a Run input and an input publication bind the epoch they
-- were admitted under (pipeline_run_inputs.go, hangar_input_publication.go),
-- so changing hangarOutput.activationEpoch strands inputs in flight. Drain
-- before changing it.

ALTER TABLE hangar_exact_lifecycles DROP CONSTRAINT hangar_exact_lifecycles_activation_epoch_fkey;
ALTER TABLE hangar_claims DROP CONSTRAINT hangar_claims_activation_epoch_fkey;
ALTER TABLE hangar_read_leases DROP CONSTRAINT hangar_read_leases_activation_epoch_fkey;
ALTER TABLE hangar_reclaim_jobs DROP CONSTRAINT hangar_reclaim_jobs_activation_epoch_fkey;
ALTER TABLE hangar_input_publications DROP CONSTRAINT hangar_input_publications_activation_epoch_fkey;
ALTER TABLE pipeline_run_inputs DROP CONSTRAINT pipeline_run_inputs_activation_epoch_fkey;
ALTER TABLE pipeline_run_input_uploads DROP CONSTRAINT pipeline_run_input_uploads_activation_epoch_fkey;

-- The inventory sweep and the operation leases its controllers held. The web's
-- reclaim pass serializes on a PostgreSQL advisory lock instead, and the orphan
-- sweep keeps no cursor: it lists the output namespace from the start each time.
DROP TABLE hangar_inventory_debt;
DROP TABLE hangar_inventory_cursors;
DROP TABLE hangar_operation_leases;
DROP FUNCTION hangar_cursor_fence();
DROP FUNCTION hangar_operation_lease_fence();

-- The activation database role's grants and the trigger that made it the only
-- writer of the epochs. The role itself is cluster-wide and was created by the
-- bootstrap, not by a migration; it is left in place holding nothing here, for
-- the operator to drop (DROP ROLE "<database>_hangar_activation").
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = hangar_activation_role_name()) THEN
        EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', hangar_activation_role_name());
        EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %I', hangar_activation_role_name());
    END IF;
END $$;

DROP TRIGGER hangar_output_activation_role_guard_truncate ON hangar_output_activation_epochs;
DROP TRIGGER hangar_output_activation_role_guard ON hangar_output_activation_epochs;
DROP FUNCTION hangar_activation_role_guard();
DROP FUNCTION hangar_activation_grants();
DROP FUNCTION hangar_activation_role_name();

DROP TABLE hangar_output_activation_epochs;
DROP FUNCTION hangar_output_epoch_transition();
DROP FUNCTION hangar_output_facet_ordinal(text);

-- A terminal capture whose node was gone or re-registered is released without
-- the node acknowledging the marker cleared (the coordinator's node-gone
-- path). The marker and its gate may remain on a node that comes back under
-- the same disk, so the release says so, and `fly hangar-status` counts it.
ALTER TABLE hangar_captures ADD COLUMN release_unacknowledged boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT hangar_captures_unacknowledged_release_is_a_release
        CHECK (NOT release_unacknowledged OR released_at IS NOT NULL);

-- hangar_output_node_keys went with the daemon-signature path; drop it here
-- for any database that still carries it.
DROP TABLE IF EXISTS hangar_output_node_keys;
