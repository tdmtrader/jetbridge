DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_credential_handoffs) THEN
        RAISE EXCEPTION 'cannot discard retained Run credential handoffs';
    END IF;
END; $$;
DROP TABLE pipeline_run_credential_handoffs;
DROP FUNCTION immutable_run_credential_handoff();
