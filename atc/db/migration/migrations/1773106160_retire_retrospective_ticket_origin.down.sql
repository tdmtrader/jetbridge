ALTER TABLE agent_tickets DROP CONSTRAINT agent_tickets_origin_check;
ALTER TABLE agent_tickets ADD CONSTRAINT agent_tickets_origin_check
    CHECK (origin IN ('web','fly','retrospective'));
