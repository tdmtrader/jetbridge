-- Restores the column shape as 1773106062 defined it. The dropped budget
-- values are gone.
ALTER TABLE agent_tickets
    ADD COLUMN budget_usd NUMERIC(12,6);
