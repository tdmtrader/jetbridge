-- Existing Runs deliberately receive no reconstructed historical definition.
CREATE TABLE pipeline_run_definitions (
    run_id bigint PRIMARY KEY REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    template_digest text NOT NULL CHECK (template_digest ~ '^[a-f0-9]{64}$'),
    config text NOT NULL,
    nonce text
);

CREATE FUNCTION guard_retained_run_definition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM pipeline_runs WHERE id = OLD.run_id) THEN
            RAISE EXCEPTION 'definition is retained for the lifetime of its Run';
        END IF;
        RETURN OLD;
    END IF;
    IF NEW.run_id IS DISTINCT FROM OLD.run_id
       OR NEW.template_digest IS DISTINCT FROM OLD.template_digest THEN
        RAISE EXCEPTION 'retained Run definition identity and attestation are immutable';
    END IF;
    -- config/nonce may be re-encrypted by the database key-rotation machinery.
    -- Every read verifies the plaintext against these immutable attestations:
    -- template_digest here and pipeline_runs.config_hash on the owning header.
    RETURN NEW;
END;
$$;
CREATE TRIGGER retained_run_definition_guard BEFORE UPDATE OR DELETE ON pipeline_run_definitions
    FOR EACH ROW EXECUTE FUNCTION guard_retained_run_definition();
