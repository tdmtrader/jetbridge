WITH removed_bindings AS (
    DELETE FROM agent_experiment_fixture_bindings
    RETURNING retention_claim_id
)
DELETE FROM agent_snapshot_retention_claims
WHERE id IN (SELECT retention_claim_id FROM removed_bindings);

DROP TABLE agent_experiment_evaluations;
DROP TABLE agent_experiment_cells;
DROP TABLE agent_experiment_evaluator_mappings;
DROP TABLE agent_experiment_control_assertions;
DROP TABLE agent_experiment_fixture_bindings;
DROP TABLE agent_experiment_fixtures;
DROP TABLE agent_experiment_variants;
DROP TABLE agent_experiments;
