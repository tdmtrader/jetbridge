-- Drop dependent foreign keys before the composite candidate keys they use.
ALTER TABLE agent_experiment_budget_reservations
    DROP CONSTRAINT agent_experiment_budget_reservations_cell_owner_fkey;

ALTER TABLE agent_experiment_cells
    DROP CONSTRAINT agent_experiment_cells_fixture_owner_fkey,
    DROP CONSTRAINT agent_experiment_cells_variant_owner_fkey;

ALTER TABLE agent_experiment_cells
    DROP CONSTRAINT agent_experiment_cells_owner_key;

ALTER TABLE agent_experiment_variants
    DROP CONSTRAINT agent_experiment_variants_owner_key;

ALTER TABLE agent_experiment_fixtures
    DROP CONSTRAINT agent_experiment_fixtures_owner_key;
