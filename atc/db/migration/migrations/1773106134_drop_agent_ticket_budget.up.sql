-- Per-ticket budgets are gone. Spend is governed by the workflow-run budget
-- reservation and the cost ledger, both keyed on workflow_run_id; this column
-- was the last of the ticket-scoped budget fallback and had no enforcer left
-- reading it.
ALTER TABLE agent_tickets DROP COLUMN budget_usd;
