-- Drop the agent checkpoint/interrupt/resume schema.
--
-- The subsystem it served has been removed: it existed to survive GKE Spot
-- preemption of an agent pod, and the spot discount it defended is worth
-- roughly $57/year against $15.64/agent-hour of model spend. It also never
-- ran -- no deployment ever set --agent-checkpoint-enabled -- so these tables
-- are empty everywhere we can observe.
--
-- Forward-only. Migrations 1773106144-47 are left in place rather than
-- amended: they have been applied durably (theborg/cicd reports all nine
-- tables present), and the migration test chain builds later migrations on
-- top of them. Amending an applied migration is the data-loss trap called out
-- in 1773106157/58's own history; this drops forward instead.
--
-- Refuse rather than destroy. Every table is expected empty. If any carries
-- rows, some deployment did enable checkpoints and the operator must decide
-- what that data is worth before it goes -- this migration will not make that
-- decision silently.
DO $$
DECLARE
    populated TEXT;
BEGIN
    SELECT string_agg(name, ', ' ORDER BY name) INTO populated
    FROM (
        SELECT 'agent_run_attempt_transcripts'  AS name, count(*) AS n FROM agent_run_attempt_transcripts
        UNION ALL SELECT 'agent_run_attempt_metrics',      count(*) FROM agent_run_attempt_metrics
        UNION ALL SELECT 'agent_run_attempt_fence_tokens', count(*) FROM agent_run_attempt_fence_tokens
        UNION ALL SELECT 'agent_run_attempts',             count(*) FROM agent_run_attempts
        UNION ALL SELECT 'agent_run_events',               count(*) FROM agent_run_events
        UNION ALL SELECT 'agent_run_effects',              count(*) FROM agent_run_effects
        UNION ALL SELECT 'agent_run_checkpoints',          count(*) FROM agent_run_checkpoints
        UNION ALL SELECT 'agent_checkpoint_objects',       count(*) FROM agent_checkpoint_objects
        UNION ALL SELECT 'agent_run_checkpoint_heads',     count(*) FROM agent_run_checkpoint_heads
    ) counts
    WHERE n > 0;

    IF populated IS NOT NULL THEN
        RAISE EXCEPTION
            'refusing to drop non-empty agent checkpoint tables: %. Checkpoints were enabled on this deployment; export or discard these rows deliberately before migrating.',
            populated;
    END IF;
END $$;

-- One statement, not nine. agent_run_checkpoint_heads.latest_checkpoint_id
-- references agent_run_checkpoints(id) while agent_run_checkpoints.head_id
-- references agent_run_checkpoint_heads(id), so no sequential order satisfies
-- both -- dropping either first raises 2BP01. Naming all nine in a single
-- DROP TABLE lets Postgres resolve the cycle, and unlike CASCADE it cannot
-- reach past the set: anything outside still depending on these would abort
-- the migration instead of being silently dropped with them.
DROP TABLE IF EXISTS
    agent_run_attempt_transcripts,
    agent_run_attempt_metrics,
    agent_run_attempt_fence_tokens,
    agent_run_attempts,
    agent_run_events,
    agent_run_effects,
    agent_run_checkpoints,
    agent_checkpoint_objects,
    agent_run_checkpoint_heads;

-- Triggers went with their tables; the functions they called did not.
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_transcript_identity();
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_metric_binding();
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_transition();
DROP FUNCTION IF EXISTS enforce_agent_run_attempt_insertion();
DROP FUNCTION IF EXISTS enforce_agent_run_event_append_only();
DROP FUNCTION IF EXISTS enforce_agent_run_effect_transition();
DROP FUNCTION IF EXISTS enforce_agent_run_checkpoint_head_identity();
DROP FUNCTION IF EXISTS agent_run_checkpoint_head_cleanup_eligible(BIGINT);
