DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pipeline_runs WHERE run_contract_version = 'v2') OR
       EXISTS (SELECT 1 FROM pipeline_run_output_starts) OR
       EXISTS (SELECT 1 FROM pipeline_run_activation WHERE admission_enabled) THEN
        RAISE EXCEPTION 'cannot remove Run start admission while v2 Runs or activation remain';
    END IF;
END $$;
DROP TABLE pipeline_run_output_starts;
DROP FUNCTION immutable_run_output_start();
DROP TABLE pipeline_run_activation;
DROP FUNCTION monotonic_run_activation();
DROP TRIGGER run_birth_contract_immutable ON pipeline_runs;
DROP FUNCTION immutable_run_birth_contract();
ALTER TABLE pipeline_runs DROP CONSTRAINT pipeline_run_birth_contract,
    DROP COLUMN activation_epoch, DROP COLUMN run_contract_version;
