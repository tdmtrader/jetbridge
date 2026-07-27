-- Remove the seed and only the seed. An operator who has since chosen a mode
-- through PUT /api/v1/agent/dispatcher owns that row (updated_by is their
-- identity, never 'migration'), and rolling the schema back must not silently
-- discard a deliberate runtime decision.

DELETE FROM agent_settings
WHERE id = 1 AND updated_by = 'migration';
