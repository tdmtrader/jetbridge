CREATE TABLE pipeline_run_invocations (
    run_id bigint PRIMARY KEY REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    team_id integer NOT NULL,
    template_pipeline_id integer NOT NULL,
    principal_digest text NOT NULL CHECK (principal_digest ~ '^[a-f0-9]{64}$'),
    key_digest text NOT NULL CHECK (key_digest ~ '^[a-f0-9]{64}$'),
    caller_document text NOT NULL,
    caller_digest text NOT NULL CHECK (caller_digest ~ '^[a-f0-9]{64}$'),
    admitted_digest text NOT NULL CHECK (admitted_digest ~ '^[a-f0-9]{64}$'),
    UNIQUE (team_id, template_pipeline_id, principal_digest, key_digest)
);

CREATE FUNCTION guard_retained_run_invocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF EXISTS (SELECT 1 FROM pipeline_runs WHERE id = OLD.run_id) THEN
            RAISE EXCEPTION 'invocation is retained for the lifetime of its Run';
        END IF;
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'retained Run invocation is immutable';
END;
$$;
CREATE TRIGGER retained_run_invocation_guard BEFORE UPDATE OR DELETE ON pipeline_run_invocations
    FOR EACH ROW EXECUTE FUNCTION guard_retained_run_invocation();
