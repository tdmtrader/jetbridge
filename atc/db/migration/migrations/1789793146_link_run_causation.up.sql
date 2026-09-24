-- A v2 Run may name one earlier Run that caused it and carry one opaque
-- correlation value. Both are birth-time caller intent: immutable, never set on
-- a legacy_v1 row, and meaningless to scheduling, cancellation, retention and
-- claims. The edge has no foreign key on purpose: purging a predecessor leaves
-- the id behind as an unresolved-predecessor marker and never cascades to the
-- successor. Pointing only at a strictly earlier id makes self and cyclic edges
-- unrepresentable.
ALTER TABLE pipeline_runs
    ADD COLUMN caused_by_run bigint,
    ADD COLUMN correlation text,
    ADD CONSTRAINT pipeline_run_causation CHECK (
        caused_by_run IS NULL OR (run_contract_version = 'v2' AND caused_by_run > 0 AND caused_by_run < id)),
    ADD CONSTRAINT pipeline_run_correlation CHECK (
        correlation IS NULL OR (run_contract_version = 'v2' AND correlation ~ '^[A-Za-z0-9._~-]{1,128}$'));

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
