-- A publication is one semantic provider operation, while an occurrence is
-- one independently authorized workflow invocation of that operation. The
-- provider key deliberately excludes run/build identity, so those identities
-- must not be stored only on the deduplicated operation row.
CREATE UNIQUE INDEX agent_publications_occurrence_parent_identity
    ON agent_publications (id, team_id, input_snapshot_id, status);

CREATE UNIQUE INDEX agent_workflow_runs_publication_occurrence_identity
    ON agent_workflow_runs (id, team_id, planned_build_id);

CREATE TABLE agent_publication_occurrences (
    id                       BIGSERIAL PRIMARY KEY,
    publication_id           BIGINT NOT NULL,
    team_id                  INTEGER NOT NULL CHECK (team_id > 0),
    team_name                TEXT NOT NULL
                               CHECK (btrim(team_name) = team_name AND team_name <> ''
                                      AND octet_length(team_name) <= 256),
    workflow_run_id          BIGINT NOT NULL,
    build_id                 BIGINT NOT NULL CHECK (build_id > 0),
    actor                    TEXT NOT NULL
                               CHECK (btrim(actor) = actor AND actor <> ''
                                      AND octet_length(actor) <= 256),
    input_snapshot_id        BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    approved_by              TEXT,
    approval_wait_id         BIGINT REFERENCES agent_workflow_waits (id) ON DELETE RESTRICT,
    approval_question_snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    approval_answer_snapshot_id   BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    approval_resolved_at     TIMESTAMPTZ,
    status                   TEXT NOT NULL
                               CHECK (status IN ('pending', 'succeeded', 'failed', 'stale_base', 'rebase_required')),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (publication_id, workflow_run_id, build_id),
    FOREIGN KEY (publication_id, team_id, input_snapshot_id, status)
        REFERENCES agent_publications (id, team_id, input_snapshot_id, status)
        ON UPDATE CASCADE ON DELETE RESTRICT,
    FOREIGN KEY (workflow_run_id, team_id, build_id)
        REFERENCES agent_workflow_runs (id, team_id, planned_build_id)
        ON DELETE RESTRICT,
    CHECK (
        (approved_by IS NOT NULL AND btrim(approved_by) <> ''
            AND approval_wait_id IS NOT NULL
            AND approval_question_snapshot_id IS NOT NULL
            AND approval_answer_snapshot_id IS NOT NULL
            AND approval_resolved_at IS NOT NULL)
        OR (approved_by IS NULL
            AND approval_wait_id IS NULL
            AND approval_question_snapshot_id IS NULL
            AND approval_answer_snapshot_id IS NULL
            AND approval_resolved_at IS NULL)
    )
);

CREATE UNIQUE INDEX agent_publication_occurrences_owner_identity
    ON agent_publication_occurrences (id, publication_id);

CREATE UNIQUE INDEX agent_publication_occurrences_outcome_identity
    ON agent_publication_occurrences
        (id, team_id, workflow_run_id, input_snapshot_id, status);

INSERT INTO agent_publication_occurrences
    (publication_id, team_id, team_name, workflow_run_id, build_id, actor,
     input_snapshot_id, approved_by, approval_wait_id,
     approval_question_snapshot_id, approval_answer_snapshot_id,
     approval_resolved_at, status, created_at, updated_at)
SELECT id, team_id, team_name, workflow_run_id, build_id, actor,
       input_snapshot_id, approved_by, approval_wait_id,
       approval_question_snapshot_id, approval_answer_snapshot_id,
       approval_resolved_at, status, created_at, updated_at
FROM agent_publications;

ALTER TABLE agent_publications
    ADD COLUMN lease_owner_occurrence_id BIGINT;

UPDATE agent_publications publication
SET lease_owner_occurrence_id = occurrence.id
FROM agent_publication_occurrences occurrence
WHERE occurrence.publication_id = publication.id;

ALTER TABLE agent_publications
    ALTER COLUMN lease_owner_occurrence_id SET NOT NULL,
    ADD CONSTRAINT agent_publications_lease_owner_occurrence_fkey
    FOREIGN KEY (lease_owner_occurrence_id, id)
        REFERENCES agent_publication_occurrences (id, publication_id)
        ON DELETE RESTRICT
        DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE agent_workflow_outcomes
    DROP CONSTRAINT agent_workflow_outcomes_publication_evidence_fkey;

DROP INDEX agent_publications_outcome_identity;

UPDATE agent_workflow_outcomes outcome
SET publication_id = occurrence.id
FROM agent_publication_occurrences occurrence
WHERE outcome.publication_id = occurrence.publication_id
  AND outcome.team_id = occurrence.team_id
  AND outcome.workflow_run_id = occurrence.workflow_run_id
  AND outcome.output_snapshot_id = occurrence.input_snapshot_id
  AND outcome.publication_status = occurrence.status;

ALTER TABLE agent_workflow_outcomes
    ADD CONSTRAINT agent_workflow_outcomes_publication_evidence_fkey
    FOREIGN KEY
        (publication_id, team_id, workflow_run_id, output_snapshot_id, publication_status)
    REFERENCES agent_publication_occurrences
        (id, team_id, workflow_run_id, input_snapshot_id, status)
    ON DELETE RESTRICT;
