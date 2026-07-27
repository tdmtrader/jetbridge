-- Feedback is a human verdict on one finding of one sealed review/v1 snapshot,
-- and that snapshot is its only identity.
--
-- The table carried two identities: the projected (review_snapshot_id,
-- review_team_id, finding_id, reviewer) key, and a legacy
-- (repo, commit_sha, finding_id, reviewer) key from the deleted v1 review
-- ingestion route. The legacy shape has been unreachable since the review
-- projection became snapshot-keyed (1773106131): agent_reviews no longer has
-- repo or commit_sha, so no review can supply either, and GetByReviewSnapshot
-- — the only read — selects on review_snapshot_id and therefore never returned
-- a legacy row. Rows written that way were accepted and then invisible forever.
--
-- The legacy rows are the owner-test corpus of that dead route. They go rather
-- than forcing the store to keep a second insert path alive for data no read
-- can reach.
DELETE FROM agent_feedback WHERE review_snapshot_id IS NULL;

DROP INDEX idx_agent_feedback_upsert;
DROP INDEX idx_agent_feedback_commit;
DROP INDEX idx_agent_feedback_repo_commit;
DROP INDEX agent_feedback_ticket;

-- repo / commit_sha: the projected writer inserted '' into both, because a
-- review/v1 record names its subjects by snapshot type and digest and carries
-- no repository coordinates at all. They existed only to satisfy the legacy
-- unique index dropped above.
--
-- ticket_id: write-orphaned. Its only source was agent_reviews.ticket_id, which
-- 1773106131 dropped. A finding's work-item context is reached through the
-- review's production and its durable workflow run.
ALTER TABLE agent_feedback
    DROP COLUMN repo,
    DROP COLUMN commit_sha,
    DROP COLUMN ticket_id;

-- With one identity left, the pair is mandatory and the CHECK that kept the two
-- columns in step is redundant with the NOT NULLs.
ALTER TABLE agent_feedback
    DROP CONSTRAINT agent_feedback_snapshot_team_pair,
    ALTER COLUMN review_snapshot_id SET NOT NULL,
    ALTER COLUMN review_team_id SET NOT NULL;

-- The partial predicates are dead weight once the columns cannot be null, and a
-- total unique index lets the single writer name a plain ON CONFLICT.
DROP INDEX agent_feedback_review_snapshot_upsert;
DROP INDEX agent_feedback_review_snapshot_created;

CREATE UNIQUE INDEX agent_feedback_review_snapshot_upsert
    ON agent_feedback (review_snapshot_id, review_team_id, finding_id, reviewer);

CREATE INDEX agent_feedback_review_snapshot_created
    ON agent_feedback (review_snapshot_id, review_team_id, created_at, id);
