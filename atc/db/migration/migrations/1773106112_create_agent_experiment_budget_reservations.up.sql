CREATE TABLE agent_experiment_budget_reservations (
    cell_id             BIGINT PRIMARY KEY
                        REFERENCES agent_experiment_cells (id) ON DELETE CASCADE,
    experiment_id       BIGINT NOT NULL
                        REFERENCES agent_experiments (id) ON DELETE CASCADE,
    reserved_usd        NUMERIC(18, 6) NOT NULL CHECK (reserved_usd >= 0),
    max_tokens          BIGINT NOT NULL DEFAULT 0 CHECK (max_tokens >= 0),
    state               TEXT NOT NULL DEFAULT 'active'
                        CHECK (state IN ('active', 'settled', 'released')),
    budget_day          DATE NOT NULL,
    settled_cost_usd    NUMERIC(18, 6) CHECK (settled_cost_usd IS NULL OR settled_cost_usd >= 0),
    settled_tokens      BIGINT CHECK (settled_tokens IS NULL OR settled_tokens >= 0),
    reserved_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    settled_at          TIMESTAMPTZ,
    CHECK (
        (state = 'active' AND settled_at IS NULL
                          AND settled_cost_usd IS NULL AND settled_tokens IS NULL)
        OR
        (state <> 'active' AND settled_at IS NOT NULL
                           AND settled_cost_usd IS NOT NULL AND settled_tokens IS NOT NULL)
    )
);

CREATE INDEX agent_experiment_budget_reservations_experiment
    ON agent_experiment_budget_reservations (experiment_id, cell_id);

CREATE INDEX agent_experiment_budget_reservations_liability_day
    ON agent_experiment_budget_reservations (budget_day, experiment_id, cell_id)
    WHERE state IN ('active', 'settled');

CREATE INDEX agent_experiment_budget_reservations_settled_at
    ON agent_experiment_budget_reservations (settled_at, cell_id)
    WHERE state = 'settled';
