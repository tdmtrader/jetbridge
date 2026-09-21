DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM pipeline_runs WHERE terminal_observation_version IS NOT NULL) THEN
  RAISE EXCEPTION 'Run terminal results must survive until governed Run purge';
 END IF;
END $$;
DROP TRIGGER run_candidate_claim_lifetime ON hangar_claims;
DROP TRIGGER run_terminal_result_match ON pipeline_runs;
DROP TRIGGER run_terminal_result_immutable ON pipeline_runs;
DROP FUNCTION check_run_candidate_claim_lifetime();
DROP FUNCTION check_run_terminal_result();
DROP FUNCTION check_run_result_claims(bigint);
DROP FUNCTION immutable_run_terminal_result();
DROP FUNCTION run_output_closed(uuid);
ALTER TABLE pipeline_runs DROP COLUMN result_manifest, DROP COLUMN terminal_observation_version;
