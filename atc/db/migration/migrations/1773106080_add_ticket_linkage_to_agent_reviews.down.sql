DROP INDEX agent_feedback_ticket;
DROP INDEX agent_reviews_ticket;

ALTER TABLE agent_feedback DROP COLUMN ticket_id;
ALTER TABLE agent_reviews  DROP COLUMN pipeline_run_id;
ALTER TABLE agent_reviews  DROP COLUMN ticket_id;
