-- Reviews derived from typed snapshots are projections. The snapshot is the
-- canonical value; these nullable links leave every legacy build review
-- untouched while allowing generic runs and uploads to project durably.
ALTER TABLE agent_reviews
    ALTER COLUMN build_id DROP NOT NULL,
    ADD COLUMN snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE CASCADE,
    ADD COLUMN workflow_run_id BIGINT REFERENCES agent_workflow_runs (id) ON DELETE SET NULL,
    ADD COLUMN production_id BIGINT REFERENCES agent_snapshot_productions (id) ON DELETE SET NULL;

-- A build may expose more than one canonical review snapshot for the same
-- repository state during parallel/reducer workflows. Keep the old key only
-- for legacy submissions; projected reviews are identified by snapshot_id.
DROP INDEX idx_agent_reviews_upsert;
CREATE UNIQUE INDEX idx_agent_reviews_upsert
    ON agent_reviews (build_id, repo, commit_sha)
    WHERE snapshot_id IS NULL;

CREATE UNIQUE INDEX agent_reviews_snapshot_unique
    ON agent_reviews (snapshot_id)
    WHERE snapshot_id IS NOT NULL;

CREATE INDEX agent_reviews_workflow_run_created
    ON agent_reviews (workflow_run_id, created_at, id)
    WHERE workflow_run_id IS NOT NULL;

CREATE INDEX agent_reviews_production
    ON agent_reviews (production_id)
    WHERE production_id IS NOT NULL;

ALTER TABLE agent_feedback
    ADD COLUMN review_snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE CASCADE,
    ADD COLUMN review_team_id INTEGER,
    ADD CONSTRAINT agent_feedback_snapshot_team_pair
        CHECK ((review_snapshot_id IS NULL) = (review_team_id IS NULL));

-- The legacy identity remains valid for legacy rows, but it must not collapse
-- two independently projected reviews that happen to describe the same
-- repository commit.
DROP INDEX idx_agent_feedback_upsert;
CREATE UNIQUE INDEX idx_agent_feedback_upsert
    ON agent_feedback (repo, commit_sha, finding_id, reviewer)
    WHERE review_snapshot_id IS NULL;

CREATE UNIQUE INDEX agent_feedback_review_snapshot_upsert
    ON agent_feedback (review_snapshot_id, review_team_id, finding_id, reviewer)
    WHERE review_snapshot_id IS NOT NULL;

CREATE INDEX agent_feedback_review_snapshot_created
    ON agent_feedback (review_snapshot_id, review_team_id, created_at, id)
    WHERE review_snapshot_id IS NOT NULL;
