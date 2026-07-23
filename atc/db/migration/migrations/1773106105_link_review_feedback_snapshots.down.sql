-- Snapshot-derived rows cannot satisfy the legacy NOT NULL build identity.
-- Remove only those disposable projections; all legacy review/feedback rows
-- remain compatible across the rollback.
DELETE FROM agent_feedback WHERE review_snapshot_id IS NOT NULL;
DELETE FROM agent_reviews WHERE snapshot_id IS NOT NULL;

DROP INDEX agent_feedback_review_snapshot_created;
DROP INDEX agent_feedback_review_snapshot_upsert;
DROP INDEX idx_agent_feedback_upsert;
ALTER TABLE agent_feedback DROP CONSTRAINT agent_feedback_snapshot_team_pair;
ALTER TABLE agent_feedback
	DROP COLUMN review_snapshot_id,
	DROP COLUMN review_team_id;
CREATE UNIQUE INDEX idx_agent_feedback_upsert
    ON agent_feedback (repo, commit_sha, finding_id, reviewer);

DROP INDEX agent_reviews_production;
DROP INDEX agent_reviews_workflow_run_created;
DROP INDEX agent_reviews_snapshot_unique;
DROP INDEX idx_agent_reviews_upsert;
ALTER TABLE agent_reviews
    DROP COLUMN production_id,
    DROP COLUMN workflow_run_id,
    DROP COLUMN snapshot_id,
    ALTER COLUMN build_id SET NOT NULL;
CREATE UNIQUE INDEX idx_agent_reviews_upsert
    ON agent_reviews (build_id, repo, commit_sha);
