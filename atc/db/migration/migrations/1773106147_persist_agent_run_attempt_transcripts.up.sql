-- Recovery creates a fresh durable attempt row even though it reuses the
-- build/plan identity. Transcript bytes must therefore identify the exact
-- server-created attempt instead of overwriting the legacy build/plan view.
--
-- Keep the denormalized identity as an audit/read model, but verify it against
-- agent_run_attempts and its immutable checkpoint head on every insert.
CREATE TABLE agent_run_attempt_transcripts (
    attempt_id BIGINT PRIMARY KEY
        REFERENCES agent_run_attempts (id) ON DELETE RESTRICT,
    build_id INTEGER NOT NULL,
    plan_id TEXT NOT NULL,
    execution_attempt INTEGER NOT NULL CHECK (execution_attempt > 0),
    workflow_run_id BIGINT,
    function_id TEXT NOT NULL DEFAULT '',
    step_name TEXT,
    ndjson TEXT NOT NULL,
    byte_len INTEGER NOT NULL CHECK (byte_len >= 0 AND byte_len = octet_length(ndjson)),
    truncated BOOLEAN NOT NULL DEFAULT FALSE,
    display_finalized BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX agent_run_attempt_transcripts_workflow_run
    ON agent_run_attempt_transcripts (workflow_run_id, created_at)
    WHERE workflow_run_id IS NOT NULL;

CREATE UNIQUE INDEX agent_run_attempt_transcripts_one_final_presentation
    ON agent_run_attempt_transcripts (build_id, plan_id)
    WHERE display_finalized;

CREATE FUNCTION enforce_agent_run_attempt_transcript_identity()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        PERFORM 1
        FROM agent_run_attempts AS a
        JOIN agent_run_checkpoint_heads AS h ON h.id = a.head_id
        WHERE a.id = NEW.attempt_id
          AND h.build_id = NEW.build_id
          AND h.plan_id = NEW.plan_id
          AND a.attempt_number = NEW.execution_attempt
          AND h.workflow_run_provenance_id IS NOT DISTINCT FROM NEW.workflow_run_id
          AND h.function_id = NEW.function_id;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'agent run attempt transcript identity does not match durable attempt';
        END IF;
        RETURN NEW;
    END IF;

    IF NEW.attempt_id <> OLD.attempt_id
       OR NEW.build_id <> OLD.build_id
       OR NEW.plan_id <> OLD.plan_id
       OR NEW.execution_attempt <> OLD.execution_attempt
       OR NEW.workflow_run_id IS DISTINCT FROM OLD.workflow_run_id
       OR NEW.function_id <> OLD.function_id
       OR NEW.step_name IS DISTINCT FROM OLD.step_name THEN
        RAISE EXCEPTION 'agent run attempt transcript identity is immutable';
    END IF;
    IF OLD.display_finalized AND NOT NEW.display_finalized THEN
        RAISE EXCEPTION 'an agent run attempt transcript final presentation is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_run_attempt_transcripts_immutable_identity
BEFORE INSERT OR UPDATE ON agent_run_attempt_transcripts
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_run_attempt_transcript_identity();
