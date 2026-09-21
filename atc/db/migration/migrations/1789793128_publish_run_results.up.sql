ALTER TABLE pipeline_runs ADD COLUMN result_manifest jsonb, ADD COLUMN terminal_observation_version text,
 ADD CONSTRAINT pipeline_run_result_shape CHECK (
  (run_contract_version='legacy_v1' AND result_manifest IS NULL AND terminal_observation_version IS NULL) OR
  (run_contract_version='v2' AND (
   (status='running' AND completed_at IS NULL AND result_manifest IS NULL AND terminal_observation_version IS NULL) OR
   (status<>'running' AND completed_at IS NOT NULL AND result_manifest IS NOT NULL AND jsonb_typeof(result_manifest)='object'
    AND terminal_observation_version IS NOT NULL AND terminal_observation_version<>''
    AND (status='succeeded' OR result_manifest='{}'::jsonb)))));

CREATE FUNCTION immutable_run_terminal_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.run_contract_version='v2' AND OLD.status<>'running' AND
   (NEW.status IS DISTINCT FROM OLD.status OR NEW.completed_at IS DISTINCT FROM OLD.completed_at OR
    NEW.result_manifest IS DISTINCT FROM OLD.result_manifest OR NEW.terminal_observation_version IS DISTINCT FROM OLD.terminal_observation_version) THEN
  RAISE EXCEPTION 'Run terminal publication is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER run_terminal_result_immutable BEFORE UPDATE ON pipeline_runs FOR EACH ROW EXECUTE FUNCTION immutable_run_terminal_result();

CREATE FUNCTION run_output_closed(handoff uuid) RETURNS boolean LANGUAGE sql STABLE AS $$
 SELECT coalesce((SELECT CASE f.disposition
  WHEN 'capture' THEN c.state IN ('registered','failed','cancelled') AND c.release_acknowledged_at IS NOT NULL AND l.handoff_id IS NOT NULL
  WHEN 'no_capture' THEN n.release_acknowledged_at IS NOT NULL AND l.handoff_id IS NOT NULL
  WHEN 'pre_reservation_cancel' THEN x.finalized_at IS NOT NULL AND (NOT x.source_reserved OR l.handoff_id IS NOT NULL)
  ELSE false END
 FROM pipeline_run_output_finishes f
 LEFT JOIN pipeline_run_output_releases l USING(handoff_id)
 LEFT JOIN hangar_capture_reservations c USING(handoff_id)
 LEFT JOIN hangar_no_capture_dispositions n USING(handoff_id)
 LEFT JOIN hangar_pre_reservation_cancel_dispositions x USING(handoff_id)
 WHERE f.handoff_id=handoff),false);
$$;

-- The application selects the complete set from the attested definition. These
-- cross-domain constraints prevent a generic claim writer bypassing its lifetime.
CREATE FUNCTION check_run_result_claims(checked_run bigint) RETURNS void LANGUAGE plpgsql AS $$
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
CREATE FUNCTION check_run_terminal_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM check_run_result_claims(NEW.id);
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_terminal_result_match AFTER INSERT OR UPDATE ON pipeline_runs
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_terminal_result();
CREATE FUNCTION check_run_candidate_claim_lifetime() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner bigint;
BEGIN
 SELECT s.run_id INTO owner FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id) WHERE c.claim_id=NEW.claim_id;
 IF owner IS NOT NULL THEN PERFORM check_run_result_claims(owner); END IF;
 RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_candidate_claim_lifetime AFTER UPDATE OF released_at ON hangar_claims
 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_candidate_claim_lifetime();
