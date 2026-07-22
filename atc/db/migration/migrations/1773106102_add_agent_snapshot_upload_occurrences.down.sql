DELETE FROM agent_snapshot_productions WHERE occurrence_kind = 'upload';

DROP INDEX agent_snapshot_productions_upload_occurrence;
DROP INDEX agent_snapshot_productions_build_occurrence;

ALTER TABLE agent_snapshot_productions
    DROP CONSTRAINT agent_snapshot_productions_occurrence_union_check,
    DROP CONSTRAINT agent_snapshot_productions_occurrence_kind_check,
    DROP COLUMN upload_idempotency_key,
    DROP COLUMN occurrence_kind,
    ALTER COLUMN build_id SET NOT NULL,
    ALTER COLUMN plan_id SET NOT NULL,
    ALTER COLUMN attempt SET NOT NULL,
    ALTER COLUMN step_kind SET NOT NULL,
    ALTER COLUMN step_name SET NOT NULL,
    ALTER COLUMN output_port SET NOT NULL;

ALTER TABLE agent_snapshot_productions
    ADD CONSTRAINT agent_snapshot_productions_build_id_plan_id_attempt_output__key
    UNIQUE (build_id, plan_id, attempt, output_port);
