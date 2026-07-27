-- The dispatcher mode is a SETTING, not a boot flag.
--
-- agent_settings was created (1773106091) with an absent row as a meaningful
-- state: "no mode chosen, fall back to --agent-dispatcher-enabled". That made
-- the effective mode a function of two authorities — a row that might not
-- exist and a flag that could differ between web nodes — so the same cluster
-- could answer the question differently depending on which node you asked and
-- whether anyone had ever touched the API.
--
-- Seeding the singleton makes the row the ONLY authority: every read finds it,
-- every mode has an author and a timestamp, and the boot flag is gone. The
-- seed is 'off' — the fail-safe. A cluster that was auto-dispatching before
-- this migration must be resumed explicitly:
--
--     fly agent dispatcher resume
--
-- ON CONFLICT DO NOTHING because an operator who already set a mode through
-- the API owns it; this migration must never overwrite a deliberate choice.

INSERT INTO agent_settings (id, dispatcher_mode, updated_at, updated_by)
VALUES (1, 'off', now(), 'migration')
ON CONFLICT (id) DO NOTHING;
