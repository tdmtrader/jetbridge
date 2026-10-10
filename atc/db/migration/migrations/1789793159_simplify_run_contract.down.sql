-- Restore the old shapes empty; dropped birth facts and digests cannot be
-- recovered. A rollback requires operators to remove Runs through team purge.
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pipeline_runs) THEN
  RAISE EXCEPTION '1789793159_simplify_run_contract rollback requires empty pipeline_runs; purge owning teams before rollback';
 END IF;
END; $$;
ALTER TABLE pipeline_runs
 DROP CONSTRAINT pipeline_run_result_shape,
 ADD COLUMN activation_epoch bigint,
 ADD COLUMN run_contract_version text NOT NULL,
 ADD COLUMN caused_by_run bigint,
 ADD COLUMN correlation text,
 ADD COLUMN terminal_observation_version text,
 ADD CONSTRAINT pipeline_run_birth_contract CHECK (run_contract_version = 'v2' AND activation_epoch IS NOT NULL AND activation_epoch > 0),
 ADD CONSTRAINT pipeline_run_causation CHECK (caused_by_run IS NULL OR (caused_by_run > 0 AND caused_by_run < id)),
 ADD CONSTRAINT pipeline_run_correlation CHECK (correlation IS NULL OR correlation ~ '^[A-Za-z0-9._~-]{1,128}$');
CREATE FUNCTION immutable_run_birth_contract() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.run_contract_version IS DISTINCT FROM OLD.run_contract_version OR
       NEW.activation_epoch IS DISTINCT FROM OLD.activation_epoch THEN
        RAISE EXCEPTION 'Run birth contract and activation epoch are immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER run_birth_contract_immutable BEFORE UPDATE ON pipeline_runs
    FOR EACH ROW EXECUTE FUNCTION immutable_run_birth_contract();

CREATE TABLE pipeline_run_activation (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    epoch bigint NOT NULL CHECK (epoch >= 0),
    admission_enabled boolean NOT NULL DEFAULT false,
    CHECK (NOT admission_enabled OR epoch > 0)
);
INSERT INTO pipeline_run_activation (epoch) VALUES (0);
CREATE FUNCTION monotonic_run_activation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Run activation history cannot be deleted';
    END IF;
    IF NEW.epoch < OLD.epoch THEN
        RAISE EXCEPTION 'Run activation epoch cannot decrease';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER run_activation_monotonic BEFORE UPDATE OR DELETE ON pipeline_run_activation
    FOR EACH ROW EXECUTE FUNCTION monotonic_run_activation();

CREATE FUNCTION immutable_run_causation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.caused_by_run IS DISTINCT FROM OLD.caused_by_run OR
       NEW.correlation IS DISTINCT FROM OLD.correlation THEN
        RAISE EXCEPTION 'Run causation and correlation are immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER run_causation_immutable BEFORE UPDATE ON pipeline_runs
    FOR EACH ROW EXECUTE FUNCTION immutable_run_causation();
CREATE OR REPLACE FUNCTION immutable_run_terminal_result() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status<>'running' AND
   (NEW.status IS DISTINCT FROM OLD.status OR NEW.completed_at IS DISTINCT FROM OLD.completed_at OR
    NEW.result_manifest IS DISTINCT FROM OLD.result_manifest OR
    NEW.terminal_observation_version IS DISTINCT FROM OLD.terminal_observation_version) THEN
  RAISE EXCEPTION 'Run terminal publication is immutable';
 END IF;
 RETURN NEW;
END;
$$;
ALTER TABLE pipeline_runs ADD CONSTRAINT pipeline_run_result_shape CHECK (
    (status = 'running' AND completed_at IS NULL AND result_manifest IS NULL AND terminal_observation_version IS NULL) OR
    (status <> 'running' AND completed_at IS NOT NULL AND result_manifest IS NOT NULL
     AND jsonb_typeof(result_manifest) = 'object' AND terminal_observation_version IS NOT NULL AND terminal_observation_version <> ''
     AND (status = 'succeeded' OR result_manifest = '{}'::jsonb)));
