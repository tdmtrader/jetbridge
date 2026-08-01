package workflowrun

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/workflowprovenance"
)

const reconciliationBatchLimit = 100

// OutputContractMismatchReason is the bounded terminal reason persisted when
// a selected build succeeds but its sealed outputs do not satisfy the frozen
// workflow signature. Consumers may classify this exact server-authored value
// without treating arbitrary error text as a contract failure.
const OutputContractMismatchReason = "workflow output contract mismatch"

var (
	errReconciliationRowFailed = errors.New("workflow-run reconciliation row failed")
	errTicketProjectionFailed  = errors.New("workflow-run ticket projection failed")
)

type ReconciliationStore interface {
	ClaimForReconciliation(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error)
	InspectForReconciliation(context.Context, snapshot.WorkflowRunID) (db.AgentWorkflowRunReconciliationView, bool, error)
	AdvanceAdmission(context.Context, snapshot.WorkflowRunID) (bool, error)
	Finalize(context.Context, db.AgentWorkflowRunFinalization) (db.AgentWorkflowRunFinalizationResult, bool, error)
}

type reconciliationReporter interface {
	RecordPass(context.Context, metric.WorkflowRunReconcilerPassResult, time.Time)
	RecordRow(context.Context, metric.WorkflowRunReconcilerRowResult)
}

type provenanceVerification struct {
	ExpectedOutputs  []db.AgentWorkflowRunExpectedOutput
	ContractMismatch bool
}

type provenanceVerifier interface {
	Verify(db.AgentWorkflowRun) (provenanceVerification, error)
}

type WaitResolutionCompleter interface {
	ReconcilePending(context.Context, int, string, snapshot.WorkflowRunID) error
}

// TicketProjector projects a finalized workflow run onto the ticket that owns
// it (Origin.Kind == OriginKindTicket, Origin.Reference == the ticket ID).
// This reconciler is the ONE place a ticket is terminalized: no ticket loop
// watches for finished work, and no dispatcher mode can switch the projection
// off. Implementations must be idempotent and must treat "the ticket already
// moved" as success — agent/dispatch.TicketProjector is the production one.
type TicketProjector interface {
	ProjectFinalizedRun(ctx context.Context, ticketID int, runID snapshot.WorkflowRunID) error
}

// NodeOccurrenceFreezer freezes the durable per-node projection of a run that
// has just terminalized. It is called from the ONE place a run becomes
// terminal, at the moment the finalizing node wins the CAS — the earliest
// point at which the outcome is settled and the latest at which the sources
// the projection derives from are still there.
//
// Nothing can reclaim those sources between the two. WorkflowRunTemplateLifecycle's
// retired pass is what cascades a run's builds and their build_events away,
// and it admits a template only when every citing agent_workflow_runs row is
// already terminal AND every pipeline_run completed more than the retirement
// period ago — so the terminal status this call site follows is a
// precondition it takes hours to satisfy, not a starting gun. Build-log
// reaping is likewise per-job and retention-bounded. The freeze is not inside
// the finalizing transaction, so the ordering is a wide practical margin
// rather than a database-level guarantee; closing it entirely would mean
// deriving the projection inside ReconciliationStore.Finalize.
//
// Implementations gather the run's authoritative evidence, call
// occurrence.Derive, and write the result. They must be idempotent, and they
// must not assume they will be called again: the reconciler deliberately does
// NOT re-attempt the freeze on a later claim of an already-terminal run, the
// way it re-attempts ticket projection. Frozen history is immutable, so a
// second attempt made after GC had reclaimed the sources would write a
// degraded projection as permanent truth, which is worse than having none.
type NodeOccurrenceFreezer interface {
	FreezeRun(ctx context.Context, run db.AgentWorkflowRun) error
}

type Reconciler struct {
	store            ReconciliationStore
	logger           lager.Logger
	admissionTimeout time.Duration
	retryDelay       time.Duration
	now              func() time.Time
	reporter         reconciliationReporter
	provenance       provenanceVerifier
	waitResolutions  WaitResolutionCompleter
	waits            WaitCanceler
	tickets          TicketProjector
	nodeOccurrences  NodeOccurrenceFreezer
}

type ReconcilerOption func(*Reconciler) error

func WithReconcilerClock(now func() time.Time) ReconcilerOption {
	return func(reconciler *Reconciler) error {
		if now == nil {
			return fmt.Errorf("workflow run reconciler: clock is required")
		}
		reconciler.now = now
		return nil
	}
}

func withReconciliationReporter(reporter reconciliationReporter) ReconcilerOption {
	return func(reconciler *Reconciler) error {
		if reporter == nil {
			return fmt.Errorf("workflow run reconciler: reporter is required")
		}
		reconciler.reporter = reporter
		return nil
	}
}

func WithWaitResolutionCompleter(completer WaitResolutionCompleter) ReconcilerOption {
	return func(reconciler *Reconciler) error {
		if nilInterface(completer) {
			return fmt.Errorf("workflow run reconciler: wait resolution completer is required")
		}
		reconciler.waitResolutions = completer
		return nil
	}
}

// WithTicketProjector installs the ticket terminalizer. Without it a
// finalized run simply has no ticket to project onto (clusters that never
// dispatch tickets); with it, every terminal outcome moves the owning ticket to
// needs_review.
func WithTicketProjector(projector TicketProjector) ReconcilerOption {
	return func(reconciler *Reconciler) error {
		if nilInterface(projector) {
			return fmt.Errorf("workflow run reconciler: ticket projector is required")
		}
		reconciler.tickets = projector
		return nil
	}
}

// WithNodeOccurrenceFreezer installs the durable node-occurrence projection.
// Without it a run still finalizes and simply keeps no per-node history.
func WithNodeOccurrenceFreezer(freezer NodeOccurrenceFreezer) ReconcilerOption {
	return func(reconciler *Reconciler) error {
		if nilInterface(freezer) {
			return fmt.Errorf("workflow run reconciler: node occurrence freezer is required")
		}
		reconciler.nodeOccurrences = freezer
		return nil
	}
}

func WithWaitCanceler(waits WaitCanceler) ReconcilerOption {
	return func(reconciler *Reconciler) error {
		if nilInterface(waits) {
			return fmt.Errorf("workflow run reconciler: wait canceler is required")
		}
		reconciler.waits = waits
		return nil
	}
}

func NewReconciler(
	store ReconciliationStore,
	logger lager.Logger,
	admissionTimeout time.Duration,
	retryDelay time.Duration,
	options ...ReconcilerOption,
) (*Reconciler, error) {
	if store == nil {
		return nil, fmt.Errorf("workflow run reconciler: store is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("workflow run reconciler: logger is required")
	}
	if admissionTimeout <= 0 {
		return nil, fmt.Errorf("workflow run reconciler: admission timeout must be positive")
	}
	if retryDelay <= 0 {
		return nil, fmt.Errorf("workflow run reconciler: retry delay must be positive")
	}
	if retryDelay > admissionTimeout/2 {
		return nil, fmt.Errorf("workflow run reconciler: admission timeout must be at least twice the retry delay")
	}

	reconciler := &Reconciler{
		store:            store,
		logger:           logger,
		admissionTimeout: admissionTimeout,
		retryDelay:       retryDelay,
		now:              time.Now,
		reporter:         otelReconciliationReporter{},
		provenance:       frozenProvenanceVerifier{},
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("workflow run reconciler: option is required")
		}
		if err := option(reconciler); err != nil {
			return nil, err
		}
	}
	return reconciler, nil
}

func (reconciler *Reconciler) Run(ctx context.Context) error {
	if ctx.Err() != nil {
		return nil
	}
	now := reconciler.now()
	claimedIDs, err := reconciler.store.ClaimForReconciliation(ctx, now, reconciler.retryDelay, reconciliationBatchLimit)
	if err != nil {
		reconciler.reporter.RecordPass(ctx, metric.WorkflowRunReconcilerPassError, now)
		return fmt.Errorf("workflow run reconciler: claim rows: %w", err)
	}
	if len(claimedIDs) > reconciliationBatchLimit {
		claimedIDs = claimedIDs[:reconciliationBatchLimit]
	}

	degraded := false
	for _, claimedID := range claimedIDs {
		if ctx.Err() != nil {
			return nil
		}
		result, err := reconciler.reconcileRow(ctx, claimedID, now)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			degraded = true
			reconciler.reporter.RecordRow(ctx, metric.WorkflowRunReconcilerRowFailed)
			reconciler.logRowFailure(claimedID, err)
			continue
		}
		reconciler.reporter.RecordRow(ctx, result)
	}

	passResult := metric.WorkflowRunReconcilerPassSuccess
	if degraded {
		passResult = metric.WorkflowRunReconcilerPassDegraded
	}
	reconciler.reporter.RecordPass(ctx, passResult, now)
	return nil
}

func (reconciler *Reconciler) reconcileRow(
	ctx context.Context,
	claimedID snapshot.WorkflowRunID,
	now time.Time,
) (metric.WorkflowRunReconcilerRowResult, error) {
	view, found, err := reconciler.store.InspectForReconciliation(ctx, claimedID)
	if err != nil {
		return "", newRowFailure("inspect", err)
	}
	if !found {
		return metric.WorkflowRunReconcilerRowAdvanced, nil
	}
	if view.Run.ID != claimedID {
		return "", newRowFailure("inspect_identity", fmt.Errorf("store returned another workflow run"))
	}

	switch view.Run.Status {
	case db.AgentWorkflowRunStatusAdmitting:
		return reconciler.reconcileAdmitting(ctx, view.Run, now)
	case db.AgentWorkflowRunStatusRunning:
		if reconciler.waitResolutions != nil {
			if err := reconciler.waitResolutions.ReconcilePending(
				ctx,
				view.Run.TeamID,
				view.Run.TeamName,
				view.Run.ID,
			); err != nil {
				return "", newRowFailure("complete_wait_resolutions", err)
			}
		}
		return reconciler.reconcileRunning(ctx, view)
	case db.AgentWorkflowRunStatusCanceling:
		if err := reconciler.cancelOpenWaits(ctx, view.Run, now); err != nil {
			return "", err
		}
		return reconciler.reconcileCanceling(ctx, view, now)
	case db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored:
		// Already terminal (another node finalized it, or this row was
		// re-claimed): re-project so a projection that failed once still lands.
		reconciler.projectOwningTicket(ctx, view.Run)
		return metric.WorkflowRunReconcilerRowAdvanced, nil
	case db.AgentWorkflowRunStatusAborted:
		if err := reconciler.cancelOpenWaits(ctx, view.Run, now); err != nil {
			return "", err
		}
		reconciler.projectOwningTicket(ctx, view.Run)
		return metric.WorkflowRunReconcilerRowAdvanced, nil
	default:
		return "", newRowFailure("invalid_status", fmt.Errorf("invalid active workflow-run status"))
	}
}

func (reconciler *Reconciler) cancelOpenWaits(
	ctx context.Context,
	run db.AgentWorkflowRun,
	now time.Time,
) error {
	if reconciler.waits == nil {
		return nil
	}
	if _, err := reconciler.waits.CancelRun(
		ctx,
		run.TeamID,
		run.ID,
		"system:workflow-run-cancel",
		now,
	); err != nil {
		return newRowFailure("cancel_open_waits", err)
	}
	return nil
}

func (reconciler *Reconciler) reconcileAdmitting(
	ctx context.Context,
	run db.AgentWorkflowRun,
	now time.Time,
) (metric.WorkflowRunReconcilerRowResult, error) {
	switch executionLinkState(run) {
	case executionLinkComplete:
		advanced, err := reconciler.store.AdvanceAdmission(ctx, run.ID)
		if err != nil {
			return "", newRowFailure("advance_admission", err)
		}
		if advanced {
			return metric.WorkflowRunReconcilerRowAdvanced, nil
		}
		return reconciler.reconcileFalseAdmissionCAS(ctx, run, now)
	case executionLinkPartial:
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "admission state is corrupt", nil)
	case executionLinkEmpty:
		if !deadlineReached(now, run.UpdatedAt, reconciler.admissionTimeout) {
			return metric.WorkflowRunReconcilerRowDeferred, nil
		}
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "admission interrupted", nil)
	default:
		return "", newRowFailure("admission_state", fmt.Errorf("unknown execution-link state"))
	}
}

func (reconciler *Reconciler) reconcileFalseAdmissionCAS(
	ctx context.Context,
	previous db.AgentWorkflowRun,
	now time.Time,
) (metric.WorkflowRunReconcilerRowResult, error) {
	view, found, err := reconciler.store.InspectForReconciliation(ctx, previous.ID)
	if err != nil {
		return "", newRowFailure("reinspect_admission", err)
	}
	if !found {
		return metric.WorkflowRunReconcilerRowAdvanced, nil
	}
	if view.Run.ID != previous.ID {
		return "", newRowFailure("reinspect_admission_identity", fmt.Errorf("store returned another workflow run"))
	}

	switch view.Run.Status {
	case db.AgentWorkflowRunStatusRunning,
		db.AgentWorkflowRunStatusSucceeded,
		db.AgentWorkflowRunStatusFailed,
		db.AgentWorkflowRunStatusErrored,
		db.AgentWorkflowRunStatusAborted:
		if view.Run.Status == db.AgentWorkflowRunStatusRunning && executionLinkState(view.Run) != executionLinkComplete {
			return "", newRowFailure("reinspect_admission_state", fmt.Errorf("advanced workflow run has incomplete execution linkage"))
		}
		return metric.WorkflowRunReconcilerRowAdvanced, nil
	case db.AgentWorkflowRunStatusCanceling:
		return reconciler.reconcileCanceling(ctx, view, now)
	case db.AgentWorkflowRunStatusAdmitting:
		switch executionLinkState(view.Run) {
		case executionLinkPartial:
			return reconciler.finalize(ctx, view.Run, db.AgentWorkflowRunStatusErrored, "admission state is corrupt", nil)
		case executionLinkEmpty:
			if !deadlineReached(now, view.Run.UpdatedAt, reconciler.admissionTimeout) {
				return metric.WorkflowRunReconcilerRowDeferred, nil
			}
			return reconciler.finalize(ctx, view.Run, db.AgentWorkflowRunStatusErrored, "admission interrupted", nil)
		case executionLinkComplete:
			if !deadlineReached(now, view.Run.UpdatedAt, reconciler.admissionTimeout) {
				return metric.WorkflowRunReconcilerRowDeferred, nil
			}
			return reconciler.finalize(ctx, view.Run, db.AgentWorkflowRunStatusErrored, "admission could not advance", nil)
		default:
			return "", newRowFailure("reinspect_admission_state", fmt.Errorf("unknown execution-link state"))
		}
	default:
		return "", newRowFailure("reinspect_admission_status", fmt.Errorf("invalid workflow-run status"))
	}
}

func (reconciler *Reconciler) reconcileRunning(
	ctx context.Context,
	view db.AgentWorkflowRunReconciliationView,
) (metric.WorkflowRunReconcilerRowResult, error) {
	run := view.Run
	if run.ExecutionStatus == nil {
		if view.SelectedBuildExists && buildIsActive(view.SelectedBuildStatus) {
			return metric.WorkflowRunReconcilerRowDeferred, nil
		}
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "execution outcome provenance lost", nil)
	}
	if !hasCompletePlanProvenance(run) {
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "workflow provenance is incomplete", nil)
	}
	return reconciler.finalizeCopiedOutcome(ctx, run)
}

func (reconciler *Reconciler) reconcileCanceling(
	ctx context.Context,
	view db.AgentWorkflowRunReconciliationView,
	now time.Time,
) (metric.WorkflowRunReconcilerRowResult, error) {
	run := view.Run
	if run.ExecutionStatus != nil {
		if *run.ExecutionStatus == db.AgentWorkflowRunExecutionStatusSucceeded && !hasCompletePlanProvenance(run) {
			return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "workflow provenance is incomplete", nil)
		}
		return reconciler.finalizeCopiedOutcome(ctx, run)
	}
	if view.SelectedBuildExists {
		if buildIsActive(view.SelectedBuildStatus) {
			return metric.WorkflowRunReconcilerRowDeferred, nil
		}
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "cancellation outcome provenance lost", nil)
	}
	if !deadlineReached(now, run.UpdatedAt, reconciler.admissionTimeout) {
		return metric.WorkflowRunReconcilerRowDeferred, nil
	}
	return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "cancellation incomplete", nil)
}

func (reconciler *Reconciler) finalizeCopiedOutcome(
	ctx context.Context,
	run db.AgentWorkflowRun,
) (metric.WorkflowRunReconcilerRowResult, error) {
	switch *run.ExecutionStatus {
	case db.AgentWorkflowRunExecutionStatusFailed:
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusFailed, "selected build failed", nil)
	case db.AgentWorkflowRunExecutionStatusErrored:
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "selected build errored", nil)
	case db.AgentWorkflowRunExecutionStatusAborted:
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusAborted, "", nil)
	case db.AgentWorkflowRunExecutionStatusSucceeded:
		verification, err := reconciler.provenance.Verify(run)
		if err != nil {
			return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusErrored, "workflow provenance is corrupt", nil)
		}
		if verification.ContractMismatch {
			return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusFailed, OutputContractMismatchReason, nil)
		}
		return reconciler.finalize(ctx, run, db.AgentWorkflowRunStatusSucceeded, "", verification.ExpectedOutputs)
	default:
		return "", newRowFailure("invalid_execution_status", fmt.Errorf("invalid copied execution status"))
	}
}

func (reconciler *Reconciler) finalize(
	ctx context.Context,
	run db.AgentWorkflowRun,
	status db.AgentWorkflowRunStatus,
	message string,
	outputs []db.AgentWorkflowRunExpectedOutput,
) (metric.WorkflowRunReconcilerRowResult, error) {
	_, applied, err := reconciler.store.Finalize(ctx, db.AgentWorkflowRunFinalization{
		WorkflowRunID:           run.ID,
		ExpectedStatus:          run.Status,
		ExpectedExecutionStatus: run.ExecutionStatus,
		ExpectedActualPlanHash:  run.ActualPlanHash,
		TerminalStatus:          status,
		ErrorMessage:            message,
		ExpectedOutputs:         outputs,
	})
	if err != nil {
		return "", newRowFailure("finalize", err)
	}
	if applied {
		// This node's CAS won, so this node owns the projection. A lost CAS
		// means another node finalized the run and projects it itself.
		//
		// The node-occurrence freeze goes first. It is the only consumer that
		// races GC for the run's evidence, and the ticket projection is a
		// cross-aggregate write that may block on another table.
		reconciler.freezeNodeOccurrences(ctx, run)
		reconciler.projectOwningTicket(ctx, run)
	}
	return metric.WorkflowRunReconcilerRowAdvanced, nil
}

// freezeNodeOccurrences writes the run's durable per-node history. Like the
// ticket projection it is deliberately not a row failure: the run IS
// finalized, and a projection fault must not re-classify that outcome or, far
// worse, strand the run in a non-terminal state forever. Losing history is
// bad; a run that can never terminalize is worse.
func (reconciler *Reconciler) freezeNodeOccurrences(ctx context.Context, run db.AgentWorkflowRun) {
	if reconciler.nodeOccurrences == nil {
		return
	}
	if err := reconciler.nodeOccurrences.FreezeRun(ctx, run); err != nil {
		if ctx.Err() != nil {
			return
		}
		reconciler.logger.Error("freeze-node-occurrences-failed", err, lager.Data{
			"workflow_run_id": run.ID.String(),
		})
	}
}

// projectOwningTicket terminalizes the ticket that owns a finished run. It is
// deliberately not a row failure: the run IS finalized, and a projection fault
// must not re-classify that outcome. It is loud in the log instead, and the
// already-terminal path above re-attempts it on any later claim.
func (reconciler *Reconciler) projectOwningTicket(ctx context.Context, run db.AgentWorkflowRun) {
	if reconciler.tickets == nil || run.OriginKind != OriginKindTicket {
		return
	}
	ticketID, err := strconv.Atoi(strings.TrimSpace(run.OriginReference))
	if err != nil || ticketID <= 0 {
		reconciler.logger.Error("ticket-origin-unreadable", errTicketProjectionFailed, lager.Data{
			"workflow_run_id":  run.ID.String(),
			"origin_reference": run.OriginReference,
		})
		return
	}
	if err := reconciler.tickets.ProjectFinalizedRun(ctx, ticketID, run.ID); err != nil {
		if ctx.Err() != nil {
			return
		}
		reconciler.logger.Error("ticket-projection-failed", err, lager.Data{
			"workflow_run_id": run.ID.String(),
			"ticket":          ticketID,
		})
	}
}

type executionLinkCompleteness uint8

const (
	executionLinkEmpty executionLinkCompleteness = iota
	executionLinkPartial
	executionLinkComplete
)

func executionLinkState(run db.AgentWorkflowRun) executionLinkCompleteness {
	any := run.PipelineRunID != nil || run.TemplatePipelineID != nil || run.InstancePipelineID != nil ||
		len(run.ConcreteConfig) != 0 || run.ConcreteConfigHash != nil || run.PlannedBuildID != nil
	if !any {
		return executionLinkEmpty
	}
	complete := run.PipelineRunID != nil && *run.PipelineRunID > 0 &&
		run.TemplatePipelineID != nil && *run.TemplatePipelineID > 0 &&
		run.InstancePipelineID != nil && *run.InstancePipelineID > 0 &&
		len(run.ConcreteConfig) != 0 &&
		run.ConcreteConfigHash != nil && strings.TrimSpace(*run.ConcreteConfigHash) != "" &&
		run.PlannedBuildID != nil && *run.PlannedBuildID > 0
	if complete {
		return executionLinkComplete
	}
	return executionLinkPartial
}

func hasCompletePlanProvenance(run db.AgentWorkflowRun) bool {
	return len(run.ActualPlan) != 0 &&
		run.ActualPlanHash != nil && strings.TrimSpace(*run.ActualPlanHash) != "" &&
		len(run.ResolvedDependencies) != 0
}

func buildIsActive(status db.BuildStatus) bool {
	return status == db.BuildStatusPending || status == db.BuildStatusStarted
}

func deadlineReached(now, updatedAt time.Time, timeout time.Duration) bool {
	return !now.Before(updatedAt.Add(timeout))
}

type rowFailure struct {
	classification string
	cause          error
}

func newRowFailure(classification string, cause error) error {
	return rowFailure{classification: classification, cause: cause}
}

func (failure rowFailure) Error() string {
	return failure.classification + ": " + failure.cause.Error()
}
func (failure rowFailure) Unwrap() error { return failure.cause }

func rowFailureClassification(err error) string {
	var failure rowFailure
	if errors.As(err, &failure) {
		return failure.classification
	}
	return "processing"
}

func (reconciler *Reconciler) logRowFailure(id snapshot.WorkflowRunID, err error) {
	reconciler.logger.Error("row-failed", errReconciliationRowFailed, lager.Data{
		"workflow_run_id": id.String(),
		"classification":  rowFailureClassification(err),
	})
}

type otelReconciliationReporter struct{}

func (otelReconciliationReporter) RecordPass(ctx context.Context, result metric.WorkflowRunReconcilerPassResult, at time.Time) {
	metric.RecordWorkflowRunReconcilerPass(ctx, result, at)
}

func (otelReconciliationReporter) RecordRow(ctx context.Context, result metric.WorkflowRunReconcilerRowResult) {
	metric.RecordWorkflowRunReconcilerRow(ctx, result)
}

type frozenProvenanceVerifier struct{}

func (frozenProvenanceVerifier) Verify(run db.AgentWorkflowRun) (provenanceVerification, error) {
	if run.ActualPlanHash == nil {
		return provenanceVerification{}, fmt.Errorf("workflow provenance actual-plan hash is missing")
	}
	captured, err := workflowprovenance.VerifyFrozen(
		workflowprovenance.Input{
			WorkflowRunID:        run.ID,
			WorkflowDefinitionID: run.WorkflowDefinitionID,
			ConcreteConfig:       run.ConcreteConfig,
		},
		run.ActualPlan,
		*run.ActualPlanHash,
		run.ResolvedDependencies,
	)
	if errors.Is(err, workflowprovenance.ErrOutputContractMismatch) {
		return provenanceVerification{ContractMismatch: true}, nil
	}
	if err != nil {
		return provenanceVerification{}, fmt.Errorf("verify frozen workflow provenance: %w", err)
	}

	outputs := make([]db.AgentWorkflowRunExpectedOutput, 0, len(captured.Outputs))
	for _, output := range captured.Outputs {
		producers := make([]db.AgentWorkflowRunExpectedProducer, 0, len(output.Producers))
		for _, producer := range output.Producers {
			producers = append(producers, db.AgentWorkflowRunExpectedProducer{
				PlanID:          producer.PlanID,
				StepKind:        producer.StepKind,
				StepName:        producer.StepName,
				LocalOutputPort: producer.LocalOutputPort,
			})
		}
		outputs = append(outputs, db.AgentWorkflowRunExpectedOutput{
			Port:                 output.Port,
			Type:                 output.Type,
			Optional:             output.Optional,
			WorkflowDefinitionID: output.WorkflowDefinitionID,
			WorkflowRunID:        output.WorkflowRunID,
			Producers:            producers,
		})
	}
	return provenanceVerification{ExpectedOutputs: outputs}, nil
}
