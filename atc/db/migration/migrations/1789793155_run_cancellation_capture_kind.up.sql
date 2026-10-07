-- run_cancellation_capture_kind: the Run cancellation queue has one capture
-- kind. Since 1789793153 a capture is one row, settled by the kind still
-- named 'handoff_classify', while 'capture_cancel_or_settle' and
-- 'source_hold_release' discover nothing. The live kind takes the capture
-- name; the two dead kinds go.
--
-- Queue rows are bookkeeping, never evidence (the owner's row is), so a row
-- of a dead kind is deleted, not carried. The progress indices index the
-- worker's kind cycle, which loses positions 2 and 3: a Run that would have
-- visited either visits build_abort next.

ALTER TABLE pipeline_run_cancellation_operations DISABLE TRIGGER run_cancellation_operation_immutable;
DELETE FROM pipeline_run_cancellation_operations WHERE kind IN ('capture_cancel_or_settle', 'source_hold_release');
UPDATE pipeline_run_cancellation_operations SET kind = 'capture_cancel_or_settle' WHERE kind = 'handoff_classify';
ALTER TABLE pipeline_run_cancellation_operations ENABLE TRIGGER run_cancellation_operation_immutable;

ALTER TABLE pipeline_run_cancellation_operations DROP CONSTRAINT pipeline_run_cancellation_operations_kind_check;
ALTER TABLE pipeline_run_cancellation_operations ADD CONSTRAINT pipeline_run_cancellation_operations_kind_check
    CHECK (kind IN ('scheduler_debt_close', 'capture_cancel_or_settle', 'build_abort',
        'executor_finish_or_stop_ack', 'candidate_settle', 'terminalize'));

DELETE FROM pipeline_run_cancellation_cursors WHERE kind IN ('capture_cancel_or_settle', 'source_hold_release');
UPDATE pipeline_run_cancellation_cursors SET kind = 'capture_cancel_or_settle' WHERE kind = 'handoff_classify';

ALTER TABLE pipeline_run_cancellation_progress
    DROP CONSTRAINT pipeline_run_cancellation_progress_next_discovery_kind_check,
    DROP CONSTRAINT pipeline_run_cancellation_progress_next_claim_kind_check;
UPDATE pipeline_run_cancellation_progress SET
    next_discovery_kind = CASE WHEN next_discovery_kind <= 2 THEN next_discovery_kind
        WHEN next_discovery_kind <= 4 THEN 2 ELSE next_discovery_kind - 2 END,
    next_claim_kind = CASE WHEN next_claim_kind <= 2 THEN next_claim_kind
        WHEN next_claim_kind <= 4 THEN 2 ELSE next_claim_kind - 2 END;
ALTER TABLE pipeline_run_cancellation_progress
    ADD CONSTRAINT pipeline_run_cancellation_progress_next_discovery_kind_check CHECK (next_discovery_kind BETWEEN 0 AND 5),
    ADD CONSTRAINT pipeline_run_cancellation_progress_next_claim_kind_check CHECK (next_claim_kind BETWEEN 0 AND 5);
