-- PR binding authority cannot be reconstructed from the legacy coordination
-- projection. In particular, the old row does not prove an accepted review,
-- an exact create_pr occurrence, or the approved repository baseline. Refuse
-- to invent those facts. Provider-native PR startup remains fail-closed, so
-- an operator must remove pre-production legacy bindings before upgrading.
LOCK TABLE agent_pr_bindings IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_pr_bindings) THEN
        RAISE EXCEPTION
            'cannot add PR binding publication authority while legacy bindings exist';
    END IF;
END
$$;

-- The originating accepted-review occurrence and the create_pr occurrence
-- must belong to the binding's exact originating workflow run.
CREATE UNIQUE INDEX agent_publication_occurrences_pr_binding_origin_identity
    ON agent_publication_occurrences (id, team_id, workflow_run_id);

ALTER TABLE agent_pr_bindings
    DROP CONSTRAINT agent_pr_bindings_originating_workflow_run_id_fkey,
    DROP CONSTRAINT agent_pr_bindings_originating_publication_occurrence_id_fkey,

    ADD COLUMN destination TEXT NOT NULL,
    ADD COLUMN approval_policy_version TEXT NOT NULL,
    ADD COLUMN creation_publication_occurrence_id BIGINT NOT NULL,
    ADD COLUMN approved_baseline_repository_snapshot_id BIGINT NOT NULL,
    ADD COLUMN approved_baseline_validation_snapshot_id BIGINT NOT NULL,
    ADD COLUMN approved_baseline_publication_occurrence_id BIGINT NOT NULL,

    ADD CONSTRAINT agent_pr_bindings_destination_check
        CHECK (
            btrim(destination) = destination
            AND destination <> ''
            AND octet_length(destination) <= 2048
        ),
    ADD CONSTRAINT agent_pr_bindings_approval_policy_version_check
        CHECK (
            btrim(approval_policy_version) = approval_policy_version
            AND approval_policy_version <> ''
            AND octet_length(approval_policy_version) <= 128
        ),

    ADD CONSTRAINT agent_pr_bindings_originating_run_team_fkey
        FOREIGN KEY (originating_workflow_run_id, team_id)
        REFERENCES agent_workflow_runs (id, team_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT agent_pr_bindings_originating_occurrence_run_team_fkey
        FOREIGN KEY (
            originating_publication_occurrence_id,
            team_id,
            originating_workflow_run_id
        )
        REFERENCES agent_publication_occurrences
            (id, team_id, workflow_run_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT agent_pr_bindings_creation_occurrence_run_team_fkey
        FOREIGN KEY (
            creation_publication_occurrence_id,
            team_id,
            originating_workflow_run_id
        )
        REFERENCES agent_publication_occurrences
            (id, team_id, workflow_run_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT agent_pr_bindings_baseline_repository_team_fkey
        FOREIGN KEY (
            approved_baseline_repository_snapshot_id,
            team_id
        )
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT agent_pr_bindings_baseline_validation_team_fkey
        FOREIGN KEY (
            approved_baseline_validation_snapshot_id,
            team_id
        )
        REFERENCES agent_snapshots (id, team_id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT agent_pr_bindings_baseline_occurrence_team_fkey
        FOREIGN KEY (
            approved_baseline_publication_occurrence_id,
            team_id
        )
        REFERENCES agent_publication_occurrences (id, team_id)
        ON DELETE RESTRICT;
