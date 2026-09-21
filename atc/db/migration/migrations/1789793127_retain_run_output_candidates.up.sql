CREATE TABLE pipeline_run_output_candidates (
    handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_finishes(handoff_id),
    claim_id uuid NOT NULL UNIQUE REFERENCES hangar_claims(claim_id),
    scope text NOT NULL,
    digest text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    receipt jsonb NOT NULL
);
CREATE TRIGGER run_output_candidate_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_candidates
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

CREATE TABLE pipeline_run_output_discards (
    handoff_id uuid PRIMARY KEY REFERENCES pipeline_run_output_finishes(handoff_id),
    reason text NOT NULL CHECK (reason = 'build_aborted')
);
CREATE TRIGGER run_output_discard_immutable BEFORE UPDATE OR DELETE ON pipeline_run_output_discards
    FOR EACH ROW EXECUTE FUNCTION immutable_run_output_evidence();

-- A Run-owned successful capture may settle only with its candidate and exact
-- retained claim. Generic release callers cannot commit around the Run adapter.
CREATE FUNCTION check_run_output_candidate() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    handoff uuid := NEW.handoff_id;
    candidate pipeline_run_output_candidates%ROWTYPE;
    registered boolean;
    settled boolean;
    discarded boolean;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pipeline_run_output_starts WHERE handoff_id=handoff) THEN
        RETURN NULL;
    END IF;
    SELECT state='registered', release_acknowledged_at IS NOT NULL INTO registered,settled
      FROM hangar_capture_reservations WHERE handoff_id=handoff;
    SELECT * INTO candidate FROM pipeline_run_output_candidates WHERE handoff_id=handoff;
    SELECT EXISTS(SELECT 1 FROM pipeline_run_output_discards WHERE handoff_id=handoff) INTO discarded;
    IF (candidate.handoff_id IS NOT NULL AND discarded) OR
       coalesce(registered AND settled,false) IS DISTINCT FROM (candidate.handoff_id IS NOT NULL OR discarded) THEN
        RAISE EXCEPTION 'Run candidate and successful capture settlement must commit together';
    END IF;
    IF candidate.handoff_id IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM hangar_claims c JOIN hangar_exact_lifecycles l ON l.id=c.lifecycle_id
        JOIN hangar_output_receipts r ON r.lifecycle_id=l.id AND r.handoff_id=handoff
        WHERE c.claim_id=candidate.claim_id AND c.released_at IS NULL
        AND c.consumer_binding_id=handoff::text
        AND l.scope=candidate.scope AND l.digest=candidate.digest AND l.generation=candidate.generation
    ) THEN
        RAISE EXCEPTION 'Run candidate requires its matching active exact-generation claim';
    END IF;
    RETURN NULL;
END;
$$;
CREATE CONSTRAINT TRIGGER run_output_candidate_match AFTER INSERT ON pipeline_run_output_candidates
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_candidate();
CREATE CONSTRAINT TRIGGER run_output_discard_match AFTER INSERT ON pipeline_run_output_discards
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_candidate();
CREATE CONSTRAINT TRIGGER run_output_candidate_match AFTER UPDATE OF release_acknowledged_at ON hangar_capture_reservations
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION check_run_output_candidate();
