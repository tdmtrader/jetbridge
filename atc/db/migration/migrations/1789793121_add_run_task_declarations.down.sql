DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_template_task_identities) THEN
        RAISE EXCEPTION 'cannot discard retained stable task identities';
    END IF;
END; $$;
DROP TABLE pipeline_template_task_identities;
DROP FUNCTION immutable_template_task_identity();
