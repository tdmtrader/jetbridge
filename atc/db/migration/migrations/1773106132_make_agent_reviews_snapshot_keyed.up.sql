-- A review is one sealed review/v1 snapshot, projected once. The snapshot ID is
-- the identity; everything else on the row is either the record's own judgment
-- or a link to the durable production occurrence.
--
-- The rows with no snapshot_id are the owner-test v1 submissions from the
-- deleted POST ingestion route. Nothing writes that shape any more and nothing
-- can write it again, so they go rather than forcing every read path to carry a
-- second shape.
DELETE FROM agent_reviews WHERE snapshot_id IS NULL;

-- The record's judgment, projected so the listing can render it without
-- decoding every stored record.json. conclusion replaces the score/max_score/
-- pass triple that reviewCompatibilityScore invented from it; severity_counts
-- replaces proven_count/observation_count, which were that same split rounded
-- to two buckets.
ALTER TABLE agent_reviews
    ADD COLUMN conclusion      TEXT  NOT NULL DEFAULT '',
    ADD COLUMN severity_counts JSONB NOT NULL DEFAULT '{}'::jsonb;

UPDATE agent_reviews SET
    conclusion = COALESCE(review #>> '{body,conclusion}', ''),
    severity_counts = CASE
        WHEN jsonb_typeof(review #> '{body,findings}') = 'array' THEN COALESCE((
            SELECT jsonb_object_agg(tally.severity, tally.total)
            FROM (
                SELECT finding ->> 'severity' AS severity, count(*) AS total
                FROM jsonb_array_elements(review #> '{body,findings}') AS finding
                WHERE finding ->> 'severity' IS NOT NULL
                GROUP BY 1
            ) AS tally
        ), '{}'::jsonb)
        ELSE '{}'::jsonb
    END;

-- The legacy (build_id, repo, commit_sha) conflict target had no writer left
-- once the v1 Upsert went, and idx_agent_reviews_repo_commit indexed columns
-- the projector only ever wrote as ''.
DROP INDEX idx_agent_reviews_upsert;
DROP INDEX idx_agent_reviews_repo_commit;
DROP INDEX agent_reviews_ticket;

-- ticket_id / pipeline_run_id: no writer. The ticket is a work-item projection
-- shell that links to the workflow run, and pipeline_run_id was a copy of
-- agent_workflow_runs.pipeline_run_id reachable through workflow_run_id.
--
-- build_id: the build linkage is agent_snapshot_productions.build_id. One
-- review snapshot can be produced in several builds, so a single column on the
-- review can only ever name one of them — and every read already preferred the
-- production's.
--
-- repo / commit_sha / branch: review/v1 names its subjects by snapshot type and
-- digest. Neither the record nor the production carries repository coordinates,
-- so the projector wrote '' into all three on purpose; they were write-orphaned
-- from the day the legacy submission path stopped writing them.
--
-- score / max_score / pass: derived from conclusion, now stored directly.
-- proven_count / observation_count: superseded by severity_counts.
-- agent_model / duration_seconds: died with the legacy submission payload's
-- metadata block; review/v1 records a judgment, not who produced it.
ALTER TABLE agent_reviews
    DROP COLUMN ticket_id,
    DROP COLUMN pipeline_run_id,
    DROP COLUMN build_id,
    DROP COLUMN repo,
    DROP COLUMN commit_sha,
    DROP COLUMN branch,
    DROP COLUMN score,
    DROP COLUMN max_score,
    DROP COLUMN pass,
    DROP COLUMN proven_count,
    DROP COLUMN observation_count,
    DROP COLUMN agent_model,
    DROP COLUMN duration_seconds,
    ALTER COLUMN snapshot_id SET NOT NULL;

-- With snapshot_id mandatory the partial predicate is dead weight, and a total
-- unique index lets the single writer name a plain ON CONFLICT (snapshot_id).
DROP INDEX agent_reviews_snapshot_unique;
CREATE UNIQUE INDEX agent_reviews_snapshot_unique ON agent_reviews (snapshot_id);
