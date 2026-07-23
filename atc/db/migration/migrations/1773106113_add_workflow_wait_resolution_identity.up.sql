ALTER TABLE agent_workflow_waits
    ADD COLUMN resolved_by_display_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN resolution_intent_answer TEXT,
    ADD COLUMN resolution_intent_actor TEXT,
    ADD COLUMN resolution_intent_display_name TEXT,
    ADD COLUMN resolution_intent_at TIMESTAMPTZ;

UPDATE agent_workflow_waits
SET resolved_by_display_name = resolved_by
WHERE status = 'resolved';

ALTER TABLE agent_workflow_waits
    ADD CONSTRAINT agent_workflow_waits_resolution_display_name_check CHECK (
        (status = 'resolved' AND btrim(resolved_by_display_name) <> '')
        OR (status <> 'resolved' AND resolved_by_display_name = '')
    ),
    ADD CONSTRAINT agent_workflow_waits_resolution_intent_check CHECK (
        (
            resolution_intent_answer IS NULL
            AND resolution_intent_actor IS NULL
            AND resolution_intent_display_name IS NULL
            AND resolution_intent_at IS NULL
        )
        OR (
            status IN ('waiting', 'resolved')
            AND btrim(resolution_intent_answer) <> ''
            AND btrim(resolution_intent_actor) <> ''
            AND btrim(resolution_intent_display_name) <> ''
            AND resolution_intent_at IS NOT NULL
        )
    );
