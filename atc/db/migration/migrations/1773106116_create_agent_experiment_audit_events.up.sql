-- Experiment mutation identity is denormalized intentionally: audit events
-- must survive future lifecycle changes to teams or experiments.
CREATE TABLE agent_experiment_audit_events (
    id            BIGSERIAL PRIMARY KEY,
    experiment_id BIGINT NOT NULL CHECK (experiment_id > 0),
    team_id       INTEGER NOT NULL CHECK (team_id > 0),
    team_name     TEXT NOT NULL
                  CHECK (btrim(team_name) = team_name AND team_name <> ''),
    action        TEXT NOT NULL CHECK (action IN ('create', 'update', 'start', 'cancel')),
    actor         TEXT NOT NULL
                  CHECK (btrim(actor) = actor AND actor <> ''
                         AND octet_length(actor) <= 256),
    revision      BIGINT NOT NULL CHECK (revision > 0),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX agent_experiment_audit_events_experiment
    ON agent_experiment_audit_events (experiment_id, id);
CREATE INDEX agent_experiment_audit_events_team_created
    ON agent_experiment_audit_events (team_id, created_at DESC, id DESC);

CREATE FUNCTION reject_agent_experiment_audit_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    RAISE EXCEPTION 'agent experiment audit events are append-only';
    RETURN NULL;
END;
$$;

CREATE TRIGGER agent_experiment_audit_events_append_only
BEFORE UPDATE OR DELETE OR TRUNCATE ON agent_experiment_audit_events
FOR EACH STATEMENT
EXECUTE FUNCTION reject_agent_experiment_audit_event_mutation();
