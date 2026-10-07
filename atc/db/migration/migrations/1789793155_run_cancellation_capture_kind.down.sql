-- Restores the eight-kind cycle. Lossy: rows of the two dead kinds deleted on
-- the way up are not recreated (they discovered nothing and finished as done).

ALTER TABLE pipeline_run_cancellation_operations DROP CONSTRAINT pipeline_run_cancellation_operations_kind_check;
ALTER TABLE pipeline_run_cancellation_operations ADD CONSTRAINT pipeline_run_cancellation_operations_kind_check
    CHECK (kind IN ('scheduler_debt_close', 'handoff_classify', 'capture_cancel_or_settle',
        'source_hold_release', 'build_abort', 'executor_finish_or_stop_ack', 'candidate_settle', 'terminalize'));

ALTER TABLE pipeline_run_cancellation_operations DISABLE TRIGGER run_cancellation_operation_immutable;
UPDATE pipeline_run_cancellation_operations SET kind = 'handoff_classify' WHERE kind = 'capture_cancel_or_settle';
ALTER TABLE pipeline_run_cancellation_operations ENABLE TRIGGER run_cancellation_operation_immutable;

UPDATE pipeline_run_cancellation_cursors SET kind = 'handoff_classify' WHERE kind = 'capture_cancel_or_settle';

ALTER TABLE pipeline_run_cancellation_progress
    DROP CONSTRAINT pipeline_run_cancellation_progress_next_discovery_kind_check,
    DROP CONSTRAINT pipeline_run_cancellation_progress_next_claim_kind_check;
UPDATE pipeline_run_cancellation_progress SET
    next_discovery_kind = CASE WHEN next_discovery_kind <= 1 THEN next_discovery_kind ELSE next_discovery_kind + 2 END,
    next_claim_kind = CASE WHEN next_claim_kind <= 1 THEN next_claim_kind ELSE next_claim_kind + 2 END;
ALTER TABLE pipeline_run_cancellation_progress
    ADD CONSTRAINT pipeline_run_cancellation_progress_next_discovery_kind_check CHECK (next_discovery_kind BETWEEN 0 AND 7),
    ADD CONSTRAINT pipeline_run_cancellation_progress_next_claim_kind_check CHECK (next_claim_kind BETWEEN 0 AND 7);
