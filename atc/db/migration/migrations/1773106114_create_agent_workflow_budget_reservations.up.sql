CREATE TABLE agent_workflow_budget_reservations (
    workflow_run_id BIGINT PRIMARY KEY
                    REFERENCES agent_workflow_runs (id) ON DELETE CASCADE,
    reserved_usd    NUMERIC(18, 6) NOT NULL CHECK (reserved_usd > 0),
    budget_day      DATE NOT NULL,
    reserved_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX agent_workflow_budget_reservations_day
    ON agent_workflow_budget_reservations (budget_day, workflow_run_id);
