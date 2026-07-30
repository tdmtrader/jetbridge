-- Older binaries cannot preserve or enforce the frozen publication target or
-- approved baseline. Serialize with binding creation and refuse to erase live
-- authority.
LOCK TABLE agent_pr_bindings IN ACCESS EXCLUSIVE MODE;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_pr_bindings) THEN
        RAISE EXCEPTION
            'cannot remove persisted PR binding publication authority while bindings exist';
    END IF;
END
$$;

ALTER TABLE agent_pr_bindings
    DROP CONSTRAINT agent_pr_bindings_baseline_occurrence_team_fkey,
    DROP CONSTRAINT agent_pr_bindings_baseline_validation_team_fkey,
    DROP CONSTRAINT agent_pr_bindings_baseline_repository_team_fkey,
    DROP CONSTRAINT agent_pr_bindings_creation_occurrence_run_team_fkey,
    DROP CONSTRAINT agent_pr_bindings_originating_occurrence_run_team_fkey,
    DROP CONSTRAINT agent_pr_bindings_originating_run_team_fkey,
    DROP COLUMN approved_baseline_publication_occurrence_id,
    DROP COLUMN approved_baseline_validation_snapshot_id,
    DROP COLUMN approved_baseline_repository_snapshot_id,
    DROP COLUMN creation_publication_occurrence_id,
    DROP COLUMN approval_policy_version,
    DROP COLUMN destination,
    ADD CONSTRAINT agent_pr_bindings_originating_workflow_run_id_fkey
        FOREIGN KEY (originating_workflow_run_id)
        REFERENCES agent_workflow_runs (id)
        ON DELETE RESTRICT,
    ADD CONSTRAINT agent_pr_bindings_originating_publication_occurrence_id_fkey
        FOREIGN KEY (originating_publication_occurrence_id)
        REFERENCES agent_publication_occurrences (id)
        ON DELETE RESTRICT;

DROP INDEX agent_publication_occurrences_pr_binding_origin_identity;
