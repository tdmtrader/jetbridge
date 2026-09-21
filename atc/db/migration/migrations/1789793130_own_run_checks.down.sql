DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id IS NOT NULL AND run_job_name IS NULL) THEN
  RAISE EXCEPTION 'cannot discard durable Run check ownership';
 END IF;
END $$;
DROP TRIGGER run_check_admission ON builds;
DROP FUNCTION guard_run_check_admission();
ALTER TABLE builds DROP CONSTRAINT builds_pipeline_run_identity_complete,
 ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
  (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL));

CREATE OR REPLACE FUNCTION check_run_result_claims(checked_run bigint) RETURNS void LANGUAGE plpgsql AS $$
DECLARE r pipeline_runs%ROWTYPE;
BEGIN
 SELECT * INTO r FROM pipeline_runs WHERE id=checked_run;
 IF r.id IS NULL OR r.run_contract_version<>'v2' THEN RETURN; END IF;
 IF EXISTS (
  SELECT 1 FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id)
  JOIN hangar_claims claim USING(claim_id)
  WHERE s.run_id=r.id AND ((claim.released_at IS NULL) IS DISTINCT FROM
   (r.status='running' OR EXISTS(SELECT 1 FROM jsonb_each(r.result_manifest) item
     WHERE item.value->>'claim_id'=c.claim_id::text)))
 ) THEN RAISE EXCEPTION 'Run publication and candidate claim lifetime must commit together'; END IF;
 IF r.status='running' THEN RETURN; END IF;
 IF EXISTS(SELECT 1 FROM pipeline_run_output_starts WHERE run_id=r.id AND NOT run_output_closed(handoff_id)) OR
    EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id=r.id AND run_job_name IS NOT NULL AND status IN ('pending','started')) THEN
  RAISE EXCEPTION 'Run terminal publication requires closed producer and build work';
 END IF;
 IF EXISTS (
  SELECT 1 FROM jsonb_each(r.result_manifest) item WHERE NOT EXISTS (
   SELECT 1 FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id)
   WHERE s.run_id=r.id AND s.result_name=item.key AND item.value=jsonb_build_object(
    'claim_id',c.claim_id::text,'ref',jsonb_build_object('scope',c.scope,'digest',c.digest,'generation',c.generation))
  )
 ) THEN RAISE EXCEPTION 'Run manifest requires its exact retained candidates'; END IF;
END;
$$;
