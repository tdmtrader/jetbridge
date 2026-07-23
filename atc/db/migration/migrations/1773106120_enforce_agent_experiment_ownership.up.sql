-- These relationships used to validate each identifier independently. That
-- allowed a cell to name an experiment while borrowing a fixture or variant
-- from another experiment, and allowed a reservation to repeat an experiment
-- that did not own its cell. Treat such rows as corruption: silently deleting
-- or re-parenting experiment evidence would make historical results untrustworthy.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_experiment_cells cell
        JOIN agent_experiment_fixtures fixture ON fixture.id = cell.fixture_id
        WHERE fixture.experiment_id <> cell.experiment_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23503',
            MESSAGE = 'agent experiment ownership migration refused: a cell fixture belongs to another experiment';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM agent_experiment_cells cell
        JOIN agent_experiment_variants variant ON variant.id = cell.variant_id
        WHERE variant.experiment_id <> cell.experiment_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23503',
            MESSAGE = 'agent experiment ownership migration refused: a cell variant belongs to another experiment';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM agent_experiment_budget_reservations reservation
        JOIN agent_experiment_cells cell ON cell.id = reservation.cell_id
        WHERE cell.experiment_id <> reservation.experiment_id
    ) THEN
        RAISE EXCEPTION USING
            ERRCODE = '23503',
            MESSAGE = 'agent experiment ownership migration refused: a budget reservation belongs to another experiment than its cell';
    END IF;
END;
$$;

ALTER TABLE agent_experiment_fixtures
    ADD CONSTRAINT agent_experiment_fixtures_owner_key
    UNIQUE (experiment_id, id);

ALTER TABLE agent_experiment_variants
    ADD CONSTRAINT agent_experiment_variants_owner_key
    UNIQUE (experiment_id, id);

ALTER TABLE agent_experiment_cells
    ADD CONSTRAINT agent_experiment_cells_owner_key
    UNIQUE (experiment_id, id);

ALTER TABLE agent_experiment_cells
    ADD CONSTRAINT agent_experiment_cells_fixture_owner_fkey
    FOREIGN KEY (experiment_id, fixture_id)
    REFERENCES agent_experiment_fixtures (experiment_id, id)
    ON DELETE RESTRICT
    NOT VALID,
    ADD CONSTRAINT agent_experiment_cells_variant_owner_fkey
    FOREIGN KEY (experiment_id, variant_id)
    REFERENCES agent_experiment_variants (experiment_id, id)
    ON DELETE RESTRICT
    NOT VALID;

ALTER TABLE agent_experiment_budget_reservations
    ADD CONSTRAINT agent_experiment_budget_reservations_cell_owner_fkey
    FOREIGN KEY (experiment_id, cell_id)
    REFERENCES agent_experiment_cells (experiment_id, id)
    ON DELETE CASCADE
    NOT VALID;

ALTER TABLE agent_experiment_cells
    VALIDATE CONSTRAINT agent_experiment_cells_fixture_owner_fkey,
    VALIDATE CONSTRAINT agent_experiment_cells_variant_owner_fkey;

ALTER TABLE agent_experiment_budget_reservations
    VALIDATE CONSTRAINT agent_experiment_budget_reservations_cell_owner_fkey;
