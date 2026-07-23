-- Generic outcomes belong to exact durable workflow outputs. They intentionally
-- remain separate from the compatibility agent_outcomes table, whose identity
-- is one mutable ticket.
CREATE UNIQUE INDEX agent_workflow_runs_id_team_id
    ON agent_workflow_runs (id, team_id);

CREATE TABLE agent_workflow_outcomes (
    team_id                 INTEGER NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    workflow_run_id         BIGINT NOT NULL,
    output_snapshot_id      BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    disposition             TEXT NOT NULL
                            CHECK (disposition IN ('accepted', 'rejected', 'merged', 'abandoned')),
    publication_state       TEXT NOT NULL
                            CHECK (publication_state IN ('not_requested', 'pending', 'published', 'failed')),
    publication_id          BIGINT CHECK (publication_id > 0),
    human_modified          BOOLEAN NOT NULL DEFAULT false,
    modification_snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    intervention_count      INTEGER NOT NULL DEFAULT 0 CHECK (intervention_count >= 0),
    labels                  JSONB NOT NULL DEFAULT '[]'::jsonb
                            CHECK (jsonb_typeof(labels) = 'array'),
    actor                   TEXT NOT NULL
                            CHECK (btrim(actor) <> '' AND actor = btrim(actor) AND octet_length(actor) <= 256),
    revision                BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    audited_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workflow_run_id, output_snapshot_id),
    FOREIGN KEY (workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id) ON DELETE CASCADE,
    CHECK (publication_id IS NULL OR publication_state <> 'not_requested'),
    CHECK (modification_snapshot_id IS NULL OR human_modified)
);

CREATE INDEX agent_workflow_outcomes_team_run
    ON agent_workflow_outcomes (team_id, workflow_run_id, output_snapshot_id);
CREATE INDEX agent_workflow_outcomes_output
    ON agent_workflow_outcomes (output_snapshot_id, workflow_run_id);
