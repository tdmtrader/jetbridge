ALTER TABLE agent_snapshot_productions
    DROP CONSTRAINT agent_snapshot_productions_build_id_plan_id_attempt_output__key;

ALTER TABLE agent_snapshot_productions
    ADD COLUMN occurrence_kind TEXT NOT NULL DEFAULT 'build',
    ADD COLUMN upload_idempotency_key TEXT,
    ALTER COLUMN build_id DROP NOT NULL,
    ALTER COLUMN plan_id DROP NOT NULL,
    ALTER COLUMN attempt DROP NOT NULL,
    ALTER COLUMN step_kind DROP NOT NULL,
    ALTER COLUMN step_name DROP NOT NULL,
    ALTER COLUMN output_port DROP NOT NULL;

ALTER TABLE agent_snapshot_productions
    ALTER COLUMN occurrence_kind DROP DEFAULT,
    ADD CONSTRAINT agent_snapshot_productions_occurrence_kind_check
        CHECK (occurrence_kind IN ('build', 'upload')),
    ADD CONSTRAINT agent_snapshot_productions_occurrence_union_check
        CHECK (
            (occurrence_kind = 'build'
                AND build_id IS NOT NULL
                AND plan_id IS NOT NULL
                AND attempt IS NOT NULL
                AND step_kind IS NOT NULL
                AND step_name IS NOT NULL
                AND output_port IS NOT NULL
                AND upload_idempotency_key IS NULL)
            OR
            (occurrence_kind = 'upload'
                AND build_id IS NULL
                AND plan_id IS NULL
                AND attempt IS NULL
                AND step_kind IS NULL
                AND step_name IS NULL
                AND output_port IS NULL
                AND workflow_definition_id IS NULL
                AND workflow_run_id IS NULL
                AND upload_idempotency_key IS NOT NULL
                AND btrim(upload_idempotency_key) <> '')
        );

CREATE UNIQUE INDEX agent_snapshot_productions_build_occurrence
    ON agent_snapshot_productions (build_id, plan_id, attempt, output_port)
    WHERE occurrence_kind = 'build';

CREATE UNIQUE INDEX agent_snapshot_productions_upload_occurrence
    ON agent_snapshot_productions (team_id, upload_idempotency_key)
    WHERE occurrence_kind = 'upload';
