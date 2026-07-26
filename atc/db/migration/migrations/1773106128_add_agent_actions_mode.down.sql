-- Restoring NOT NULL needs a value for any row the switch created without a
-- dispatcher mode. 'off' is the effective mode such a row already reports when
-- --agent-dispatcher-enabled is unset; on a cluster booted with the flag set
-- this down migration therefore pins the dispatcher off until an admin sets a
-- mode again. That is deliberate: fail dormant, not fail dispatching.
--
-- The backfill is a DECISION this migration makes, so it signs it: without the
-- provenance columns the GET status API would attribute 'off' to whichever
-- admin last touched the row, and an operator debugging a dormant dispatcher
-- would have no way to see that a rollback, not a person, stopped it. This is
-- also ONE-WAY: re-applying the up migration cannot tell a backfilled 'off'
-- from an explicit one, so "dispatcher unset (boot flag applies)" is gone for
-- good and an admin must set the mode again deliberately.
UPDATE agent_settings
SET dispatcher_mode = 'off',
    updated_at = now(),
    updated_by = 'migration-1773106128-down'
WHERE dispatcher_mode IS NULL;
ALTER TABLE agent_settings ALTER COLUMN dispatcher_mode SET NOT NULL;

ALTER TABLE agent_settings
    DROP COLUMN actions_updated_by,
    DROP COLUMN actions_updated_at,
    DROP COLUMN actions_mode;
