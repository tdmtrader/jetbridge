-- A pre-cutover row keeps its original semantics. No historical v2 claim is
-- inferred from status, timestamps, or the presence of a retained definition.
ALTER TABLE pipeline_runs
    ADD COLUMN run_contract_version text NOT NULL DEFAULT 'legacy_v1',
    ADD COLUMN activation_epoch bigint,
    ADD CONSTRAINT pipeline_run_birth_contract CHECK (
        (run_contract_version = 'legacy_v1' AND activation_epoch IS NULL) OR
        (run_contract_version = 'v2' AND activation_epoch IS NOT NULL AND activation_epoch > 0));

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

-- No application route enables this marker yet. Enabling public v2 admission
-- requires the joint Run/cancellation/Hangar/retention checkpoint.
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

CREATE TABLE pipeline_run_output_starts (
    run_id bigint NOT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
    -- Build identity survives reclamation of the disposable payload.
    build_id integer NOT NULL,
    task_id uuid NOT NULL REFERENCES pipeline_template_task_identities(task_id),
    result_name text NOT NULL,
    task_name text NOT NULL,
    node_name text NOT NULL CHECK (node_name <> ''),
    node_uid text NOT NULL CHECK (node_uid <> ''),
    handoff_id uuid NOT NULL UNIQUE REFERENCES hangar_handoff_predeclarations(handoff_id),
    PRIMARY KEY (build_id, task_id)
);
CREATE INDEX pipeline_run_output_starts_run ON pipeline_run_output_starts (run_id, build_id);
CREATE FUNCTION immutable_run_output_start() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'Run output start identity is immutable until Run purge is integrated';
    END IF;
    IF NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'Run output start identity is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER run_output_start_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_starts
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_start();
