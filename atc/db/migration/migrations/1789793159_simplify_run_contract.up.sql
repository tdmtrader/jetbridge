-- C7 changes the attested invocation encoding. Refuse retained evidence rather
-- than silently invalidating replay or rewriting rows protected against updates.
LOCK TABLE pipeline_run_invocations IN ACCESS EXCLUSIVE MODE;
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pipeline_run_invocations) THEN
  RAISE EXCEPTION '1789793159_simplify_run_contract requires empty pipeline_run_invocations; stop web nodes and truncate retained invocation evidence as documented in MIGRATING.md before retrying';
 END IF;
END; $$;

DROP TRIGGER run_birth_contract_immutable ON pipeline_runs;
DROP FUNCTION immutable_run_birth_contract();
DROP TRIGGER run_causation_immutable ON pipeline_runs;
DROP FUNCTION immutable_run_causation();
DROP TABLE pipeline_run_activation;
DROP FUNCTION monotonic_run_activation();
ALTER TABLE pipeline_runs
 DROP CONSTRAINT pipeline_run_birth_contract,
 DROP CONSTRAINT pipeline_run_causation,
 DROP CONSTRAINT pipeline_run_correlation,
 DROP CONSTRAINT pipeline_run_result_shape;
CREATE OR REPLACE FUNCTION immutable_run_terminal_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status<>'running' AND
   (NEW.status IS DISTINCT FROM OLD.status OR NEW.completed_at IS DISTINCT FROM OLD.completed_at OR
    NEW.result_manifest IS DISTINCT FROM OLD.result_manifest) THEN
  RAISE EXCEPTION 'Run terminal publication is immutable';
 END IF;
 RETURN NEW;
END;
$$;
ALTER TABLE pipeline_runs
 DROP COLUMN activation_epoch,
 DROP COLUMN run_contract_version,
 DROP COLUMN caused_by_run,
 DROP COLUMN correlation,
 DROP COLUMN terminal_observation_version;
ALTER TABLE pipeline_runs ADD CONSTRAINT pipeline_run_result_shape CHECK (
    (status = 'running' AND completed_at IS NULL AND result_manifest IS NULL) OR
    (status <> 'running' AND completed_at IS NOT NULL AND result_manifest IS NOT NULL
     AND jsonb_typeof(result_manifest) = 'object'
     AND (status = 'succeeded' OR result_manifest = '{}'::jsonb)));

-- run_terminal_result_match and check_run_result_claims do not reference the
-- removed fields. Keep their deferred exact-candidate/claim and closure checks.
