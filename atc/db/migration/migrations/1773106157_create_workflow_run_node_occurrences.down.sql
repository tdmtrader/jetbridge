DROP INDEX IF EXISTS agent_publication_occurrences_plan;
ALTER TABLE agent_publication_occurrences DROP COLUMN IF EXISTS plan_id;

DROP TRIGGER IF EXISTS agent_workflow_run_node_occurrences_immutable
    ON agent_workflow_run_node_occurrences;
DROP FUNCTION IF EXISTS enforce_agent_workflow_run_node_occurrence_immutability();
DROP TABLE IF EXISTS agent_workflow_run_node_occurrences;
