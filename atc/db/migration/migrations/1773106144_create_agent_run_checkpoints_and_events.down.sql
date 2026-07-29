DROP INDEX IF EXISTS agent_run_events_head_created;
DROP INDEX IF EXISTS agent_checkpoint_objects_deletion;
DROP INDEX IF EXISTS agent_checkpoint_objects_reconciliation;
DROP INDEX IF EXISTS agent_run_checkpoints_expiration;

DROP TRIGGER IF EXISTS agent_run_events_append_only ON agent_run_events;
DROP FUNCTION IF EXISTS enforce_agent_run_event_append_only();

DROP TRIGGER IF EXISTS agent_run_effects_monotonic ON agent_run_effects;
DROP FUNCTION IF EXISTS enforce_agent_run_effect_transition();

DROP FUNCTION IF EXISTS agent_run_checkpoint_head_cleanup_eligible(BIGINT);

DROP TRIGGER IF EXISTS agent_run_checkpoint_heads_immutable_identity ON agent_run_checkpoint_heads;
DROP FUNCTION IF EXISTS enforce_agent_run_checkpoint_head_identity();

DROP TABLE IF EXISTS agent_run_events;
DROP TABLE IF EXISTS agent_run_effects;

ALTER TABLE agent_run_checkpoint_heads
DROP CONSTRAINT IF EXISTS agent_run_checkpoint_heads_latest;

DROP INDEX IF EXISTS agent_run_checkpoints_one_staged;
DROP INDEX IF EXISTS agent_run_checkpoints_one_committed;
DROP TABLE IF EXISTS agent_run_checkpoints;
DROP TABLE IF EXISTS agent_checkpoint_objects;
DROP TABLE IF EXISTS agent_run_checkpoint_heads;
