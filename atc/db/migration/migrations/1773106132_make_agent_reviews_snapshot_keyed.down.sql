-- Restores the column SHAPE and the legacy indexes so an older binary can write
-- the v1 review row again. The dropped values are gone, and the v1 rows this
-- migration deleted are not resurrected: rolling back recovers the schema, not
-- the owner-test corpus.
DROP INDEX agent_reviews_snapshot_unique;

ALTER TABLE agent_reviews
    ALTER COLUMN snapshot_id DROP NOT NULL,
    ADD COLUMN ticket_id         INTEGER,
    ADD COLUMN pipeline_run_id   INTEGER,
    ADD COLUMN build_id          INTEGER,
    ADD COLUMN repo              TEXT NOT NULL DEFAULT '',
    ADD COLUMN commit_sha        TEXT NOT NULL DEFAULT '',
    ADD COLUMN branch            TEXT NOT NULL DEFAULT '',
    ADD COLUMN score             DOUBLE PRECISION NOT NULL DEFAULT 0,
    ADD COLUMN max_score         DOUBLE PRECISION NOT NULL DEFAULT 10,
    ADD COLUMN pass              BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN proven_count      INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN observation_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN agent_model       TEXT NOT NULL DEFAULT '',
    ADD COLUMN duration_seconds  INTEGER NOT NULL DEFAULT 0,
    DROP COLUMN conclusion,
    DROP COLUMN severity_counts;

CREATE UNIQUE INDEX agent_reviews_snapshot_unique
    ON agent_reviews (snapshot_id)
    WHERE snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX idx_agent_reviews_upsert
    ON agent_reviews (build_id, repo, commit_sha)
    WHERE snapshot_id IS NULL;

CREATE INDEX idx_agent_reviews_repo_commit ON agent_reviews (repo, commit_sha);

CREATE INDEX agent_reviews_ticket ON agent_reviews (ticket_id) WHERE ticket_id IS NOT NULL;
