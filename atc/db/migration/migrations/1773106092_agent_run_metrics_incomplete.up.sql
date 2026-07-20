-- L-1 (#41): a flight ingestion that reads NO flight output records
-- status = 'incomplete' — a missing RECORDING (dominant cause: a runner image
-- predating the flight recorder), not a failed step. DeriveOutcome fuses it to
-- the amber 'unrecorded' outcome on a succeeded build (never red).
ALTER TABLE agent_run_metrics DROP CONSTRAINT agent_run_metrics_status_check;
ALTER TABLE agent_run_metrics ADD CONSTRAINT agent_run_metrics_status_check
    CHECK (status IN ('ok','failed','error','parked','incomplete'));
