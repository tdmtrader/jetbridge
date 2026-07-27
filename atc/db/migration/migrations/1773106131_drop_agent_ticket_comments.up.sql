-- The ticket comment/answer surface is gone: its three Store methods
-- (AppendComment/AnswerComment/Comments) had no HTTP route, no fly command and
-- no Elm caller, and the only remaining reader was the work-item capture's
-- LATERAL sub-select — which now emits no comments key at all. Nothing can
-- write or read this table, so it goes with the code.
DROP TABLE IF EXISTS agent_ticket_comments;
