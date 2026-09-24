-- Two amendment M-2 decisions of the durable Run contract.
--
-- Decision 1. The legacy_v1 Run class is retired (durable Run contract, amendment M-2
-- decision 1): "clean code, not persisted compatibility". Every legacy_v1 Run
-- is dropped with everything that hangs off it -- its builds and their events,
-- its payload pipeline, its retained definition, and any composition call left
-- with no iteration once the Run's iteration cascades away. Only v2 Runs
-- remain, and the schema admits no other class.
--
-- Run evidence and payloads are otherwise undeletable; the transaction-local
-- team-purge marker (1773105509, 1789793145) is the one allowance the guards
-- honour, and it ends with this migration's transaction. A legacy_v1 Run has
-- no v2 evidence, bound input, claim, invocation or cancellation row: the
-- schema refused them for it.
SELECT set_config('concourse.pipeline_run_team_purge', 'on', true);

CREATE TEMPORARY TABLE retired_runs ON COMMIT DROP AS
    SELECT id FROM pipeline_runs WHERE run_contract_version <> 'v2';

DELETE FROM build_events
    WHERE build_id IN (SELECT id FROM builds WHERE pipeline_run_id IN (SELECT id FROM retired_runs));
DELETE FROM builds WHERE pipeline_run_id IN (SELECT id FROM retired_runs);
DELETE FROM pipelines WHERE pipeline_run_id IN (SELECT id FROM retired_runs);
DELETE FROM pipeline_run_definitions WHERE run_id IN (SELECT id FROM retired_runs);
DELETE FROM pipeline_runs WHERE id IN (SELECT id FROM retired_runs);
DELETE FROM composition_calls call
    WHERE NOT EXISTS (SELECT 1 FROM composition_iterations i WHERE i.call_id = call.id);

SELECT set_config('concourse.pipeline_run_team_purge', '', true);

-- One class. The column stays as a birth-time field (invocation spec,
-- amendment A2(a)): a future contract class needs no migration to be named,
-- and no admission path may branch on it to relax finality.
ALTER TABLE pipeline_runs
    ALTER COLUMN run_contract_version DROP DEFAULT,
    DROP CONSTRAINT pipeline_run_birth_contract,
    ADD CONSTRAINT pipeline_run_birth_contract CHECK (
        run_contract_version = 'v2' AND activation_epoch IS NOT NULL AND activation_epoch > 0),
    DROP CONSTRAINT pipeline_run_result_shape,
    ADD CONSTRAINT pipeline_run_result_shape CHECK (
        (status = 'running' AND completed_at IS NULL AND result_manifest IS NULL AND terminal_observation_version IS NULL) OR
        (status <> 'running' AND completed_at IS NOT NULL AND result_manifest IS NOT NULL AND jsonb_typeof(result_manifest) = 'object'
         AND terminal_observation_version IS NOT NULL AND terminal_observation_version <> ''
         AND (status = 'succeeded' OR result_manifest = '{}'::jsonb))),
    DROP CONSTRAINT run_cancel_request_shape,
    ADD CONSTRAINT run_cancel_request_shape CHECK (
        (cancel_requested_at IS NULL AND cancel_requested_by IS NULL AND cancel_reason IS NULL) OR
        (cancel_requested_at IS NOT NULL AND cancel_requested_by IS NOT NULL
         AND cancel_requested_by <> '' AND status IN ('running', 'aborted')
         AND (cancel_reason IS NULL OR octet_length(cancel_reason) BETWEEN 1 AND 512))),
    DROP CONSTRAINT pipeline_run_causation,
    ADD CONSTRAINT pipeline_run_causation CHECK (
        caused_by_run IS NULL OR (caused_by_run > 0 AND caused_by_run < id)),
    DROP CONSTRAINT pipeline_run_correlation,
    ADD CONSTRAINT pipeline_run_correlation CHECK (
        correlation IS NULL OR correlation ~ '^[A-Za-z0-9._~-]{1,128}$');

-- The terminal-result guard, the run-check admission guard and the
-- result-claim check stop asking which class a Run is: there is one. Otherwise
-- unchanged from 1789793128, 1789793144 and 1789793130.
CREATE OR REPLACE FUNCTION immutable_run_terminal_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status<>'running' AND
   (NEW.status IS DISTINCT FROM OLD.status OR NEW.completed_at IS DISTINCT FROM OLD.completed_at OR
    NEW.result_manifest IS DISTINCT FROM OLD.result_manifest OR NEW.terminal_observation_version IS DISTINCT FROM OLD.terminal_observation_version) THEN
  RAISE EXCEPTION 'Run terminal publication is immutable';
 END IF;
 RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION guard_run_check_admission() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner pipeline_runs%ROWTYPE;
BEGIN
 IF NEW.pipeline_run_id IS NULL OR NEW.run_job_name IS NOT NULL THEN RETURN NEW; END IF;
 IF NEW.resource_id IS NULL AND NEW.resource_type_id IS NULL THEN
  RAISE EXCEPTION 'Run check admission requires a resource or resource type';
 END IF;
 SELECT * INTO owner FROM pipeline_runs WHERE id=NEW.pipeline_run_id FOR NO KEY UPDATE;
 IF owner.id IS NULL OR owner.status<>'running' OR owner.cancel_requested_at IS NOT NULL OR
    NOT EXISTS(SELECT 1 FROM pipelines WHERE id=NEW.pipeline_id AND pipeline_run_id=owner.id) THEN
  RAISE EXCEPTION 'Run check is not admitted';
 END IF;
 RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION check_run_result_claims(checked_run bigint) RETURNS void LANGUAGE plpgsql AS $$
DECLARE r pipeline_runs%ROWTYPE;
BEGIN
 SELECT * INTO r FROM pipeline_runs WHERE id=checked_run;
 IF r.id IS NULL THEN RETURN; END IF;
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


-- Decision 3. A Run's activation epoch is the Run contract's own, no longer the
-- Hangar output epoch. A bound input's claim still carries the Hangar epoch it
-- was acquired under, and its lifecycle that same epoch; the Run's birth epoch
-- is simply not that number any more, so it is no longer compared. Otherwise
-- unchanged from 1789793139.
CREATE OR REPLACE FUNCTION check_run_input_claim(checked_claim uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE binding pipeline_run_inputs%ROWTYPE;
BEGIN
    SELECT * INTO binding FROM pipeline_run_inputs WHERE claim_id=checked_claim;
    IF binding.run_id IS NULL THEN RETURN; END IF;
    IF NOT EXISTS (
        SELECT 1 FROM hangar_claims c JOIN hangar_exact_lifecycles l ON l.id=c.lifecycle_id
        JOIN pipeline_runs r ON r.id=binding.run_id
        WHERE c.claim_id=checked_claim AND c.released_at IS NULL
        AND c.consumer_binding_id=checked_claim::text
        AND l.scope=binding.scope AND l.digest=binding.digest AND l.generation=binding.generation
        AND c.activation_epoch=binding.activation_epoch AND l.activation_epoch=binding.activation_epoch
    ) THEN RAISE EXCEPTION 'Run input requires its retained exact-generation claim'; END IF;
END;
$$;
