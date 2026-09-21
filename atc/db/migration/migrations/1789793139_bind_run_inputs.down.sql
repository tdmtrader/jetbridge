DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_run_inputs) THEN
        RAISE EXCEPTION 'cannot discard retained Run input claims';
    END IF;
END; $$;
DROP TRIGGER run_input_claim_lifetime ON hangar_claims;
DROP TABLE pipeline_run_inputs;
DROP FUNCTION check_retained_run_input();
DROP FUNCTION check_run_input_claim(uuid);
