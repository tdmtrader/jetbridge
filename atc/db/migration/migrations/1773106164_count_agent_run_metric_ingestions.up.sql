-- Count how many times one (build_id, plan_id) has been ingested.
--
-- agent_run_metrics is upserted on that key, so a step that runs more than
-- once -- a web restart that could not reattach, an interruption followed by a
-- fresh agent -- overwrites its own row and leaves no trace of the earlier
-- execution. Two things need that trace.
--
-- Bounding interruption restarts needs it. Nothing on the Retriable path
-- counts anything: the engine returns without finishing, the build tracker
-- re-picks it up, and for an agent whose pod is gone each cycle starts a new
-- agent at full token spend. The cap has to survive the web that was counting,
-- so it lives here rather than in memory.
--
-- The cost ledger needs it too. A ledger row is charged the delta against the
-- replaced row, which is right for a resumed ingestion of the same execution
-- and wrong for a genuinely new one -- the abandoned agent's spend simply
-- disappears. A caller that can see the sequence advance can tell the two
-- apart.
--
-- Backfill to 1, not 0: every existing row was ingested at least once, and a
-- zero would read as "never ingested" to anything comparing against the cap.
ALTER TABLE agent_run_metrics
    ADD COLUMN ingestion_seq INTEGER NOT NULL DEFAULT 1
        CHECK (ingestion_seq > 0);
