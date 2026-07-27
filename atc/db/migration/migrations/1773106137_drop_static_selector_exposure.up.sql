-- Exposure lineage keeps its one honest answer and loses the half nothing could
-- ever produce.
--
-- The platform materializes whole artifacts: atc/exec/task_step.go and
-- atc/exec/agent_step.go record every mount with snapshot.FullTreeExposure, and
-- 1773106127's backfill wrote 'full' for every historical row. No code path has
-- ever constructed a static selector, so agent_snapshot_exposure_paths is empty
-- everywhere and the second mode is a promise the runtime cannot keep. Keeping an
-- enumerable-path schema for it invites a reader to believe partial exposure is
-- recorded when it never is.
--
-- Dropping the path table takes the composite-foreign-key parent index with it,
-- and the mode check narrows to the only value the Go type still admits
-- (agent/snapshot/exposure.go, MaterializationFull).

DROP TABLE agent_snapshot_exposure_paths;

DROP INDEX agent_snapshot_exposures_mode_identity;

ALTER TABLE agent_snapshot_exposures
    DROP CONSTRAINT agent_snapshot_exposures_materialization_mode_check;

ALTER TABLE agent_snapshot_exposures
    ADD CONSTRAINT agent_snapshot_exposures_materialization_mode_check
    CHECK (materialization_mode = 'full');
