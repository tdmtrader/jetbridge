-- PARK-V2 (shared-contracts §1.8, 2026-07-10 amendment): a park-exit partial
-- ingestion writes status = 'parked' — a defined end awaiting a human, not an
-- error — plus the latest claude session id for the execution.
-- The inline CHECK in 1773106060 got the default constraint name.
ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked'));
ALTER TABLE agent_run_metrics ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
