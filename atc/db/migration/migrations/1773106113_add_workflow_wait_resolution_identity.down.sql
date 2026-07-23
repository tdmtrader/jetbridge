ALTER TABLE agent_workflow_waits
    DROP CONSTRAINT agent_workflow_waits_resolution_intent_check,
    DROP CONSTRAINT agent_workflow_waits_resolution_display_name_check,
    DROP COLUMN resolution_intent_at,
    DROP COLUMN resolution_intent_display_name,
    DROP COLUMN resolution_intent_actor,
    DROP COLUMN resolution_intent_answer,
    DROP COLUMN resolved_by_display_name;
