CREATE TABLE agent_feedback (
    id               SERIAL PRIMARY KEY,
    repo             TEXT NOT NULL,
    commit_sha       TEXT NOT NULL,
    finding_id       TEXT NOT NULL,
    finding_type     TEXT NOT NULL DEFAULT '',
    finding_snapshot JSONB,
    verdict          TEXT NOT NULL,
    confidence       DOUBLE PRECISION NOT NULL DEFAULT 0,
    notes            TEXT NOT NULL DEFAULT '',
    reviewer         TEXT NOT NULL,
    source           TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_agent_feedback_upsert
    ON agent_feedback(repo, commit_sha, finding_id, reviewer);

CREATE INDEX idx_agent_feedback_commit
    ON agent_feedback(commit_sha);

CREATE INDEX idx_agent_feedback_repo_commit
    ON agent_feedback(repo, commit_sha);
