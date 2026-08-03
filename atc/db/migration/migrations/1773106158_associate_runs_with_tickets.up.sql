-- A ticket may drive many runs across many workflows, while a run belongs to
-- at most one ticket. agent_tickets.workflow_run_id modelled the inverse and
-- could only ever name one run, so later retries and follow-on workflows were
-- unattributable.
--
-- The live FK plus immutable copied evidence follows the same idiom as
-- agent_workflow_waits.build_id / build_id_evidence: the reference survives
-- deletion or archival of the mutable intake ticket, so durable run history
-- never loses the context that explains it.
ALTER TABLE agent_workflow_runs
    ADD COLUMN ticket_id BIGINT REFERENCES agent_tickets (id) ON DELETE SET NULL,
    ADD COLUMN ticket_reference TEXT NOT NULL DEFAULT '';

ALTER TABLE agent_workflow_runs
    ADD CONSTRAINT agent_workflow_runs_ticket_evidence_check
        CHECK (ticket_id IS NULL OR btrim(ticket_reference) <> '');

CREATE INDEX agent_workflow_runs_ticket_journal
    ON agent_workflow_runs (ticket_id, created_at DESC, id DESC)
    WHERE ticket_id IS NOT NULL;

-- text_pattern_ops, not the default text_ops: this index exists for the run
-- list's `ticket_reference LIKE $1` prefix search, and PostgreSQL can only turn
-- a LIKE prefix into an index range scan when the operator class is
-- pattern-aware or the database collation is C. The deployed cluster runs
-- initdb with the image's en_US.utf8, so under the default class this index
-- would be dead for the one query it was created to serve and the search would
-- degrade to scanning the team's whole run population.
CREATE INDEX agent_workflow_runs_ticket_reference
    ON agent_workflow_runs (ticket_reference text_pattern_ops, created_at DESC)
    WHERE ticket_reference <> '';

-- agent_tickets.workflow_run_id is not merely dropped: the one fact it still
-- carried that this column does not is "the run of the ticket's CURRENT
-- dispatch attempt", which dispatch already admits under the ticket's
-- reservation key as the run's idempotency key. That read becomes a derived
-- lookup instead of a second stored truth, so it needs this index.
CREATE INDEX agent_workflow_runs_idempotency_key
    ON agent_workflow_runs (idempotency_key);

-- Adopt the association the old column already expressed.
UPDATE agent_workflow_runs r
SET ticket_id = t.id,
    ticket_reference = CASE
        WHEN btrim(t.external_ref) <> '' THEN t.external_ref
        ELSE 'ticket-' || t.id::text
    END
FROM agent_tickets t
WHERE t.workflow_run_id = r.id;

-- The dropped column named at most ONE run, and the pre-migration Transition
-- code cleared it on every unqueue and requeue, so on a real database it holds
-- the current dispatch's run at best and NULL for any ticket sitting in draft
-- or queued. Backfilling from it alone would orphan every earlier dispatch of
-- every requeued ticket — permanently, because the trigger below forbids
-- adoption after the fact — which is the exact loss this migration exists to
-- end ("a ticket drives many runs ... later retries and follow-on workflows
-- were unattributable").
--
-- Dispatch stamps every ticket-driven run with origin_kind 'ticket' and the
-- ticket ID as origin_reference (agent/dispatch), so that evidence is exact and
-- already indexed by (team_id, origin_kind, origin_reference). It is read here
-- as evidence of an association that already happened, not as a second source
-- of truth going forward: admission sets ticket_id explicitly from now on.
UPDATE agent_workflow_runs r
SET ticket_id = t.id,
    ticket_reference = CASE
        WHEN btrim(t.external_ref) <> '' THEN t.external_ref
        ELSE 'ticket-' || t.id::text
    END
FROM agent_tickets t
WHERE r.ticket_id IS NULL
  AND r.origin_kind = 'ticket'
  AND btrim(r.origin_reference) = t.id::text;

-- Association is decided at admission and is audit evidence thereafter.
CREATE FUNCTION enforce_agent_workflow_run_ticket_immutability()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- ON DELETE SET NULL must still be able to clear the live reference; only
    -- the durable evidence and re-pointing are forbidden.
    IF NEW.ticket_reference IS DISTINCT FROM OLD.ticket_reference THEN
        RAISE EXCEPTION 'workflow run ticket association is immutable';
    END IF;
    IF OLD.ticket_id IS NOT NULL
       AND NEW.ticket_id IS NOT NULL
       AND NEW.ticket_id <> OLD.ticket_id THEN
        RAISE EXCEPTION 'workflow run ticket association is immutable';
    END IF;
    IF OLD.ticket_id IS NULL AND NEW.ticket_id IS NOT NULL THEN
        RAISE EXCEPTION 'workflow run ticket association is immutable';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_workflow_runs_ticket_immutable
BEFORE UPDATE OF ticket_id, ticket_reference ON agent_workflow_runs
FOR EACH ROW
EXECUTE FUNCTION enforce_agent_workflow_run_ticket_immutability();

-- One truth. Keeping both directions would let them disagree.
DROP INDEX IF EXISTS agent_tickets_workflow_run;
ALTER TABLE agent_tickets DROP COLUMN workflow_run_id;
