ALTER TABLE agent_reviews  ADD COLUMN ticket_id INTEGER;        -- NULL = plain CI review (today's rows)
ALTER TABLE agent_reviews  ADD COLUMN pipeline_run_id INTEGER;
ALTER TABLE agent_feedback ADD COLUMN ticket_id INTEGER;

CREATE INDEX agent_reviews_ticket  ON agent_reviews  (ticket_id) WHERE ticket_id IS NOT NULL;
CREATE INDEX agent_feedback_ticket ON agent_feedback (ticket_id) WHERE ticket_id IS NOT NULL;
