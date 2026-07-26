-- Restoring NOT NULL needs a value for any row the switch created without a
-- dispatcher mode. 'off' is the effective mode such a row already reports when
-- --agent-dispatcher-enabled is unset; on a cluster booted with the flag set
-- this down migration therefore pins the dispatcher off until an admin sets a
-- mode again. That is deliberate: fail dormant, not fail dispatching.
UPDATE agent_settings SET dispatcher_mode = 'off' WHERE dispatcher_mode IS NULL;
ALTER TABLE agent_settings ALTER COLUMN dispatcher_mode SET NOT NULL;

ALTER TABLE agent_settings
    DROP COLUMN actions_updated_by,
    DROP COLUMN actions_updated_at,
    DROP COLUMN actions_mode;
