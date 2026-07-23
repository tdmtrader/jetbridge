ALTER TABLE agent_workflow_outcomes
    DROP CONSTRAINT agent_workflow_outcomes_publication_evidence_fkey;

-- The old schema can represent only the operation's first workflow authority.
-- Fail closed for later-run aliases when downgrading instead of attaching
-- their outcome to a publication row owned by another run.
UPDATE agent_workflow_outcomes outcome
SET publication_state = 'not_requested',
    publication_id = NULL,
    publication_status = NULL
FROM agent_publication_occurrences occurrence
JOIN agent_publications publication ON publication.id = occurrence.publication_id
WHERE outcome.publication_id = occurrence.id
  AND (
      occurrence.team_id <> publication.team_id
      OR occurrence.workflow_run_id <> publication.workflow_run_id
      OR occurrence.input_snapshot_id <> publication.input_snapshot_id
      OR occurrence.status <> publication.status
  );

UPDATE agent_workflow_outcomes outcome
SET publication_id = occurrence.publication_id
FROM agent_publication_occurrences occurrence
WHERE outcome.publication_id = occurrence.id;

-- A pending operation may have been reclaimed by a later occurrence. Preserve
-- that exact active authority in the legacy single-row representation so a
-- downgrade cannot resume execution under the first run's stale credentials.
UPDATE agent_publications publication
SET team_name = owner.team_name,
    workflow_run_id = owner.workflow_run_id,
    build_id = owner.build_id,
    actor = owner.actor,
    approved_by = owner.approved_by,
    approval_wait_id = owner.approval_wait_id,
    approval_question_snapshot_id = owner.approval_question_snapshot_id,
    approval_answer_snapshot_id = owner.approval_answer_snapshot_id,
    approval_resolved_at = owner.approval_resolved_at
FROM agent_publication_occurrences owner
WHERE publication.lease_owner_occurrence_id = owner.id
  AND publication.status = 'pending';

CREATE UNIQUE INDEX agent_publications_outcome_identity
    ON agent_publications
        (id, team_id, workflow_run_id, input_snapshot_id, status);

ALTER TABLE agent_workflow_outcomes
    ADD CONSTRAINT agent_workflow_outcomes_publication_evidence_fkey
    FOREIGN KEY
        (publication_id, team_id, workflow_run_id, output_snapshot_id, publication_status)
    REFERENCES agent_publications
        (id, team_id, workflow_run_id, input_snapshot_id, status)
    ON DELETE RESTRICT;

ALTER TABLE agent_publications
    DROP CONSTRAINT agent_publications_lease_owner_occurrence_fkey,
    DROP COLUMN lease_owner_occurrence_id;

DROP TABLE agent_publication_occurrences;

DROP INDEX agent_workflow_runs_publication_occurrence_identity;
DROP INDEX agent_publications_occurrence_parent_identity;
