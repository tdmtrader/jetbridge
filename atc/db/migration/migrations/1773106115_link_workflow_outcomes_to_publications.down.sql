ALTER TABLE agent_workflow_outcomes
    DROP CONSTRAINT agent_workflow_outcomes_publication_evidence_fkey,
    DROP CONSTRAINT agent_workflow_outcomes_publication_evidence_shape,
    DROP COLUMN publication_status;

DROP INDEX agent_publications_outcome_identity;
