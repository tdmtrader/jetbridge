ALTER TABLE agent_workflow_outcomes
    ADD COLUMN publication_status TEXT;

CREATE UNIQUE INDEX agent_publications_outcome_identity
    ON agent_publications
        (id, team_id, workflow_run_id, input_snapshot_id, status);

-- Preserve only pre-integrity claims that already identify the exact succeeded
-- publication for the same team, run, and snapshot. Everything else was a
-- human-observed assertion or an arbitrary numeric ID and fails closed.
UPDATE agent_workflow_outcomes outcome
SET publication_status = 'succeeded'
FROM agent_publications publication
WHERE outcome.publication_state = 'published'
  AND outcome.publication_id = publication.id
  AND outcome.team_id = publication.team_id
  AND outcome.workflow_run_id = publication.workflow_run_id
  AND outcome.output_snapshot_id = publication.input_snapshot_id
  AND publication.status = 'succeeded';

UPDATE agent_workflow_outcomes
SET publication_state = 'not_requested',
    publication_id = NULL,
    publication_status = NULL
WHERE (
    (publication_state = 'not_requested'
        AND publication_id IS NULL
        AND publication_status IS NULL)
    OR
    (publication_state = 'published'
        AND publication_id IS NOT NULL
        AND publication_status = 'succeeded')
) IS NOT TRUE;

ALTER TABLE agent_workflow_outcomes
    ADD CONSTRAINT agent_workflow_outcomes_publication_evidence_shape
    CHECK ((
        (publication_state = 'not_requested'
            AND publication_id IS NULL
            AND publication_status IS NULL)
        OR
        (publication_state = 'published'
            AND publication_id IS NOT NULL
            AND publication_status = 'succeeded')
    ) IS TRUE),
    ADD CONSTRAINT agent_workflow_outcomes_publication_evidence_fkey
    FOREIGN KEY
        (publication_id, team_id, workflow_run_id, output_snapshot_id, publication_status)
    REFERENCES agent_publications
        (id, team_id, workflow_run_id, input_snapshot_id, status)
    ON DELETE RESTRICT;
