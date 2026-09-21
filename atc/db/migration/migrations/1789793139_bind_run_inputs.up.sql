CREATE TABLE pipeline_run_inputs (
    run_id bigint NOT NULL REFERENCES pipeline_runs(id) ON DELETE RESTRICT,
    name text NOT NULL CHECK (name <> '' AND octet_length(name) <= 256),
    source_run_id bigint NOT NULL CHECK (source_run_id > 0 AND source_run_id < run_id),
    source_result text NOT NULL CHECK (source_result <> ''),
    scope text NOT NULL,
    digest text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    claim_id uuid NOT NULL UNIQUE REFERENCES hangar_claims(claim_id),
    activation_epoch bigint NOT NULL REFERENCES hangar_output_activation_epochs(epoch_id),
    PRIMARY KEY (run_id,name)
);
CREATE TRIGGER run_input_immutable BEFORE UPDATE OR DELETE ON pipeline_run_inputs
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

CREATE FUNCTION check_run_input_claim(checked_claim uuid) RETURNS void LANGUAGE plpgsql AS $$
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
        AND r.activation_epoch=binding.activation_epoch
        AND r.run_contract_version='v2'
    ) THEN RAISE EXCEPTION 'Run input requires its retained exact-generation claim'; END IF;
END;
$$;
CREATE FUNCTION check_retained_run_input() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM check_run_input_claim(NEW.claim_id);
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_input_claim_match AFTER INSERT ON pipeline_run_inputs
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_retained_run_input();
CREATE CONSTRAINT TRIGGER run_input_claim_lifetime AFTER UPDATE OF released_at ON hangar_claims
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_retained_run_input();
