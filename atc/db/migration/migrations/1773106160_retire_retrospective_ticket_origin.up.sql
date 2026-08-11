-- ValidOrigin (agent/api/tickets/types.go) has accepted only 'web' and 'fly'
-- since the retrospective self-filing principal was retired: nothing in the
-- application can create a 'retrospective'-origin ticket anymore. 1773106136
-- narrowed this same CHECK to drop 'jira' but left 'retrospective' in the
-- allowed list regardless, so the database has stayed more permissive than
-- the application ever since -- a direct write, or a future application
-- regression, could still produce a row nothing downstream expects to see.
--
-- Refuse to run against a database that already holds one: silently folding
-- a real ticket's recorded origin onto 'web', the way 1773106136 did for
-- 'jira', would misstate how it was actually filed. Unlike 'jira' -- which
-- the create handler always rejected, so no row could exist -- a
-- 'retrospective' ticket really could have been filed before the principal
-- that created them was retired, so its provenance is worth a human's
-- judgment call rather than a migration silently rewriting history.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_tickets WHERE origin = 'retrospective') THEN
        RAISE EXCEPTION 'agent_tickets origin tightening refused: a retrospective-origin ticket still exists'
            USING ERRCODE = '23514';
    END IF;
END;
$$;

ALTER TABLE agent_tickets DROP CONSTRAINT agent_tickets_origin_check;
ALTER TABLE agent_tickets ADD CONSTRAINT agent_tickets_origin_check
    CHECK (origin IN ('web','fly'));
