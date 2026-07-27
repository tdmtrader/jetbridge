-- The ticket becomes a pure QUEUE shell.
--
-- Its state used to mix two different questions: "where is this work item in
-- the queue" (draft/queued/running/needs_review) and "how did the run turn
-- out" (merged / merged_with_fixes / sent_back / concluded / abandoned /
-- failed / errored). The second question is answered by the durable workflow
-- run and its agent_workflow_outcomes row, which is the ONLY disposition
-- record; mirroring it onto the ticket produced two truths that drifted.
--
-- Existing rows are folded onto the queue lifecycle:
--   * the terminal dispositions become 'closed' — the human is done with the
--     work item, and WHY is read from the run;
--   * sent_back / failed / errored become 'needs_review' — a human still owns
--     the decision (re-queue or close), which is exactly what needs_review
--     means, and their completed_at is cleared because they are not finished.
--
-- error_detail goes with the 'errored' state: it was a per-ticket copy of the
-- run's failure text, written only on that transition and readable in full on
-- the workflow run.
--
-- agent_ticket_specs and agent_ticket_tasks go too. Both were written only by
-- the agent-sidecar submit routes deleted earlier in this cleanup; nothing can
-- write either table any more, and their only readers were the ticket detail
-- payload and the work-item capture. The ticket's markdown body is now the
-- single place ticket prose lives, so the newest spec body is folded into it
-- before the table is dropped.

UPDATE agent_tickets t
SET body = CASE
        WHEN btrim(t.body) = '' THEN s.body
        ELSE t.body || E'\n\n---\n\n' || s.body
    END
FROM (
    SELECT DISTINCT ON (ticket_id) ticket_id, body
    FROM agent_ticket_specs
    ORDER BY ticket_id, version DESC
) s
WHERE s.ticket_id = t.id
  AND btrim(s.body) <> '';

DROP TABLE IF EXISTS agent_ticket_tasks;
DROP TABLE IF EXISTS agent_ticket_specs;

ALTER TABLE agent_tickets DROP CONSTRAINT agent_tickets_state_check;

UPDATE agent_tickets
SET state = 'closed'
WHERE state IN ('merged', 'merged_with_fixes', 'concluded', 'abandoned');

UPDATE agent_tickets
SET state = 'needs_review',
    completed_at = NULL
WHERE state IN ('sent_back', 'failed', 'errored');

ALTER TABLE agent_tickets
    ADD CONSTRAINT agent_tickets_state_check
    CHECK (state IN ('draft', 'queued', 'running', 'needs_review', 'closed'));

ALTER TABLE agent_tickets DROP COLUMN error_detail;
