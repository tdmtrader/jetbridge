-- Restores the column SHAPE and the legacy indexes so an older binary can write
-- the legacy repo/commit feedback row again. The dropped values are gone, and
-- the legacy rows the up-migration deleted are not resurrected: rolling back
-- recovers the schema, not the owner-test corpus.

DROP INDEX agent_feedback_review_snapshot_upsert;
DROP INDEX agent_feedback_review_snapshot_created;

ALTER TABLE agent_feedback
    ALTER COLUMN review_snapshot_id DROP NOT NULL,
    ALTER COLUMN review_team_id DROP NOT NULL,
    ADD CONSTRAINT agent_feedback_snapshot_team_pair
        CHECK ((review_snapshot_id IS NULL) = (review_team_id IS NULL));

ALTER TABLE agent_feedback
    ADD COLUMN repo       TEXT NOT NULL DEFAULT '',
    ADD COLUMN commit_sha TEXT NOT NULL DEFAULT '',
    ADD COLUMN ticket_id  INTEGER;

CREATE UNIQUE INDEX agent_feedback_review_snapshot_upsert
    ON agent_feedback (review_snapshot_id, review_team_id, finding_id, reviewer)
    WHERE review_snapshot_id IS NOT NULL;

CREATE INDEX agent_feedback_review_snapshot_created
    ON agent_feedback (review_snapshot_id, review_team_id, created_at, id)
    WHERE review_snapshot_id IS NOT NULL;

CREATE UNIQUE INDEX idx_agent_feedback_upsert
    ON agent_feedback (repo, commit_sha, finding_id, reviewer)
    WHERE review_snapshot_id IS NULL;

CREATE INDEX idx_agent_feedback_commit
    ON agent_feedback (commit_sha);

CREATE INDEX idx_agent_feedback_repo_commit
    ON agent_feedback (repo, commit_sha);

CREATE INDEX agent_feedback_ticket
    ON agent_feedback (ticket_id) WHERE ticket_id IS NOT NULL;
