ALTER TABLE builds DROP CONSTRAINT builds_pipeline_run_identity_complete,
 ADD CONSTRAINT builds_pipeline_run_identity_complete CHECK (
  (pipeline_run_id IS NULL AND run_job_name IS NULL AND run_job_key IS NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NOT NULL AND run_job_key IS NOT NULL) OR
  (pipeline_run_id IS NOT NULL AND run_job_name IS NULL AND run_job_key IS NULL AND job_id IS NULL
   AND (resource_id IS NOT NULL OR resource_type_id IS NOT NULL)));

CREATE FUNCTION guard_run_check_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner pipeline_runs%ROWTYPE;
BEGIN
 IF NEW.pipeline_run_id IS NULL OR (NEW.resource_id IS NULL AND NEW.resource_type_id IS NULL) THEN RETURN NEW; END IF;
 SELECT * INTO owner FROM pipeline_runs WHERE id=NEW.pipeline_run_id FOR NO KEY UPDATE;
 IF owner.run_contract_version<>'v2' OR owner.status<>'running' OR owner.cancel_requested_at IS NOT NULL OR
    NOT EXISTS(SELECT 1 FROM pipelines WHERE id=NEW.pipeline_id AND pipeline_run_id=owner.id) THEN
  RAISE EXCEPTION 'Run check is not admitted';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER run_check_admission BEFORE INSERT ON builds FOR EACH ROW EXECUTE FUNCTION guard_run_check_admission();

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
    EXISTS(SELECT 1 FROM builds WHERE pipeline_run_id=r.id AND status IN ('pending','started')) THEN
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
