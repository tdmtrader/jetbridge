package db

import "time"

// RunCancellationKind names a concrete piece of existing Run-owned work. Queue
// completion is bookkeeping, never evidence of executor or source quiescence.
type RunCancellationKind string

const (
	CancelSchedulerDebt RunCancellationKind = "scheduler_debt_close"
	CancelHandoff       RunCancellationKind = "handoff_classify"
	CancelCapture       RunCancellationKind = "capture_cancel_or_settle"
	CancelSourceHold    RunCancellationKind = "source_hold_release"
	CancelBuild         RunCancellationKind = "build_abort"
	CancelExecution     RunCancellationKind = "executor_finish_or_stop_ack"
	CancelCandidate     RunCancellationKind = "candidate_settle"
	CancelTerminalize   RunCancellationKind = "terminalize"
)

type RunCancellationDebt string

const (
	CancellationDone        RunCancellationDebt = ""
	CancellationInterrupted RunCancellationDebt = "interrupted"
	CancellationPending     RunCancellationDebt = "pending"
	CancellationUnavailable RunCancellationDebt = "unavailable"
	CancellationTimeout     RunCancellationDebt = "timeout"
	CancellationConflict    RunCancellationDebt = "conflict"
)

type RunCancellationOperation struct {
	ID          int64
	RunID       int
	Kind        RunCancellationKind
	Subject     string
	Attempt     int64
	WorkerEpoch int64
	DatabaseNow time.Time
	Deadline    time.Time
}

const (
	RunCancellationRunLimit       = 50
	RunCancellationOperationLimit = 100
	RunCancellationPassLimit      = 30 * time.Second
	RunCancellationOperationTerm  = 10 * time.Second
)
