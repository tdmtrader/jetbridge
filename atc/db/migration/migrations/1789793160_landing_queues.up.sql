-- The landing queue's own tables (ADR-0009). One queue per (team, name); the
-- config is the strictly decoded YAML the API accepted.
CREATE TABLE landing_queues (
    id bigserial PRIMARY KEY,
    team_id integer NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    name text NOT NULL,
    config jsonb NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now(),
    UNIQUE (team_id, name)
);

-- One row per submitted change. (queue_id, entry_id) is the dedup: a second
-- submit of the same id with the same commit is a no-op on this index, with
-- another commit a conflict. state is queued | in_flight | landed | ejected;
-- landed and ejected are final.
CREATE TABLE landing_entries (
    id bigserial PRIMARY KEY,
    queue_id bigint NOT NULL REFERENCES landing_queues(id) ON DELETE CASCADE,
    entry_id text NOT NULL,
    commit text NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','in_flight','landed','ejected')),
    submitted_by text NOT NULL DEFAULT '',
    submitted_at timestamp with time zone NOT NULL DEFAULT now(),
    settled_at timestamp with time zone,
    settle_reason text NOT NULL DEFAULT '',
    compose_run_id bigint REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    land_run_id bigint REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    UNIQUE (queue_id, entry_id)
);

-- One landing intent per candidate: created when the compose Run is admitted,
-- keyed by it, and completed by the land Run, so a land Run exists at most
-- once for a candidate however many times the component restarts. state is
-- composing | landing | done; fails counts land Runs that did not succeed in
-- a row, cleared by a landing.
CREATE TABLE landing_intents (
    id bigserial PRIMARY KEY,
    queue_id bigint NOT NULL REFERENCES landing_queues(id) ON DELETE CASCADE,
    entry_id bigint NOT NULL REFERENCES landing_entries(id) ON DELETE CASCADE,
    compose_run_id bigint NOT NULL UNIQUE REFERENCES pipeline_runs(id) ON DELETE CASCADE,
    land_run_id bigint UNIQUE REFERENCES pipeline_runs(id) ON DELETE SET NULL,
    state text NOT NULL DEFAULT 'composing' CHECK (state IN ('composing','landing','done')),
    fails integer NOT NULL DEFAULT 0,
    last_error text NOT NULL DEFAULT '',
    created_at timestamp with time zone NOT NULL DEFAULT now(),
    updated_at timestamp with time zone NOT NULL DEFAULT now()
);
CREATE INDEX landing_intents_open ON landing_intents (queue_id) WHERE state <> 'done';
