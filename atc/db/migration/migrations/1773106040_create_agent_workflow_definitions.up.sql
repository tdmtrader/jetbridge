CREATE TABLE agent_workflow_definitions (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    version      INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    definition   TEXT NOT NULL,
    live         BOOLEAN NOT NULL DEFAULT false,
    description  TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    promoted_at  TIMESTAMPTZ,
    promoted_by  TEXT NOT NULL DEFAULT '',
    UNIQUE (name, version)
);

CREATE UNIQUE INDEX agent_workflow_definitions_live ON agent_workflow_definitions (name) WHERE live;
CREATE UNIQUE INDEX agent_workflow_definitions_hash ON agent_workflow_definitions (name, content_hash);
