-- Publication evidence is an authorization boundary. Give each referenced
-- durable identity a same-team key so a raw write cannot associate evidence
-- owned by another team.
CREATE UNIQUE INDEX agent_snapshots_id_team_publication_evidence
    ON agent_snapshots (id, team_id);

CREATE UNIQUE INDEX agent_publication_occurrences_id_team_evidence
    ON agent_publication_occurrences (id, team_id);

CREATE UNIQUE INDEX agent_workflow_waits_id_team_publication_evidence
    ON agent_workflow_waits (id, team_id);

CREATE TABLE agent_publication_inputs (
    publication_id BIGINT NOT NULL,
    team_id        INTEGER NOT NULL,
    role           TEXT NOT NULL
        CHECK (role IN ('observation', 'validation', 'impact', 'response')),
    snapshot_id    BIGINT NOT NULL,
    PRIMARY KEY (publication_id, role),
    FOREIGN KEY (publication_id, team_id)
        REFERENCES agent_publication_occurrences (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT
);

CREATE INDEX agent_publication_inputs_snapshot
    ON agent_publication_inputs (team_id, snapshot_id, publication_id, role);

CREATE TABLE agent_publication_approval_evidence (
    publication_id         BIGINT PRIMARY KEY,
    team_id                INTEGER NOT NULL,
    evidence_kind          TEXT NOT NULL
        CHECK (evidence_kind IN ('accepted_review', 'human_wait')),

    review_snapshot_id     BIGINT,
    candidate_snapshot_id  BIGINT,
    validation_snapshot_id BIGINT,
    review_workflow_run_id BIGINT,
    outcome_revision       BIGINT CHECK (outcome_revision > 0),
    accepted_by            TEXT
        CHECK (accepted_by IS NULL
            OR (btrim(accepted_by) = accepted_by
                AND accepted_by <> ''
                AND octet_length(accepted_by) <= 256)),
    accepted_at            TIMESTAMPTZ,

    human_wait_id          BIGINT,
    question_snapshot_id   BIGINT,
    answer_snapshot_id     BIGINT,
    resolved_by            TEXT
        CHECK (resolved_by IS NULL
            OR (btrim(resolved_by) = resolved_by
                AND resolved_by <> ''
                AND octet_length(resolved_by) <= 256)),
    resolved_at            TIMESTAMPTZ,

    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    FOREIGN KEY (publication_id, team_id)
        REFERENCES agent_publication_occurrences (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (review_snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (candidate_snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (validation_snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (review_workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (human_wait_id, team_id)
        REFERENCES agent_workflow_waits (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (question_snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    FOREIGN KEY (answer_snapshot_id, team_id)
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,

    CHECK (
        (
            evidence_kind = 'accepted_review'
            AND review_snapshot_id IS NOT NULL
            AND candidate_snapshot_id IS NOT NULL
            AND validation_snapshot_id IS NOT NULL
            AND review_workflow_run_id IS NOT NULL
            AND outcome_revision IS NOT NULL
            AND accepted_by IS NOT NULL
            AND accepted_at IS NOT NULL
            AND human_wait_id IS NULL
            AND question_snapshot_id IS NULL
            AND answer_snapshot_id IS NULL
            AND resolved_by IS NULL
            AND resolved_at IS NULL
        )
        OR
        (
            evidence_kind = 'human_wait'
            AND review_snapshot_id IS NULL
            AND candidate_snapshot_id IS NULL
            AND validation_snapshot_id IS NULL
            AND review_workflow_run_id IS NULL
            AND outcome_revision IS NULL
            AND accepted_by IS NULL
            AND accepted_at IS NULL
            AND human_wait_id IS NOT NULL
            AND question_snapshot_id IS NOT NULL
            AND answer_snapshot_id IS NOT NULL
            AND resolved_by IS NOT NULL
            AND resolved_at IS NOT NULL
        )
    )
);

INSERT INTO agent_publication_approval_evidence (
    publication_id, team_id, evidence_kind, human_wait_id,
    question_snapshot_id, answer_snapshot_id, resolved_by, resolved_at
)
SELECT id, team_id, 'human_wait', approval_wait_id,
       approval_question_snapshot_id, approval_answer_snapshot_id,
       approved_by, approval_resolved_at
FROM agent_publication_occurrences
WHERE approval_wait_id IS NOT NULL;
