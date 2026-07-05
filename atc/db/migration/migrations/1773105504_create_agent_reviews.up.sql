CREATE TABLE agent_reviews (
    id                SERIAL PRIMARY KEY,
    build_id          INTEGER NOT NULL,
    build_name        TEXT NOT NULL DEFAULT '',
    team_name         TEXT NOT NULL,
    pipeline_name     TEXT NOT NULL DEFAULT '',
    job_name          TEXT NOT NULL DEFAULT '',
    repo              TEXT NOT NULL,
    commit_sha        TEXT NOT NULL,
    branch            TEXT NOT NULL DEFAULT '',
    score             DOUBLE PRECISION NOT NULL DEFAULT 0,
    max_score         DOUBLE PRECISION NOT NULL DEFAULT 10,
    pass              BOOLEAN NOT NULL DEFAULT false,
    proven_count      INTEGER NOT NULL DEFAULT 0,
    observation_count INTEGER NOT NULL DEFAULT 0,
    summary           TEXT NOT NULL DEFAULT '',
    agent_model       TEXT NOT NULL DEFAULT '',
    duration_seconds  INTEGER NOT NULL DEFAULT 0,
    review            JSONB NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_agent_reviews_upsert
    ON agent_reviews(build_id, repo, commit_sha);

CREATE INDEX idx_agent_reviews_team_created
    ON agent_reviews(team_name, created_at DESC);

CREATE INDEX idx_agent_reviews_repo_commit
    ON agent_reviews(repo, commit_sha);
