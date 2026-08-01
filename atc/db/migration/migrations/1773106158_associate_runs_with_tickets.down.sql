ALTER TABLE agent_tickets
    ADD COLUMN workflow_run_id BIGINT REFERENCES agent_workflow_runs (id) ON DELETE SET NULL;

-- Restore only the first run per ticket: the old column could never hold more.
UPDATE agent_tickets t
SET workflow_run_id = (
    SELECT r.id FROM agent_workflow_runs r
    WHERE r.ticket_id = t.id
    ORDER BY r.created_at, r.id
    LIMIT 1
);

CREATE UNIQUE INDEX agent_tickets_workflow_run
    ON agent_tickets (workflow_run_id)
    WHERE workflow_run_id IS NOT NULL;

DROP TRIGGER IF EXISTS agent_workflow_runs_ticket_immutable ON agent_workflow_runs;
DROP FUNCTION IF EXISTS enforce_agent_workflow_run_ticket_immutability();
DROP INDEX IF EXISTS agent_workflow_runs_idempotency_key;
DROP INDEX IF EXISTS agent_workflow_runs_ticket_reference;
DROP INDEX IF EXISTS agent_workflow_runs_ticket_journal;
ALTER TABLE agent_workflow_runs
    DROP CONSTRAINT IF EXISTS agent_workflow_runs_ticket_evidence_check;
ALTER TABLE agent_workflow_runs
    DROP COLUMN IF EXISTS ticket_reference,
    DROP COLUMN IF EXISTS ticket_id;
