package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workitem"
	"github.com/concourse/concourse/atc/db"
)

var (
	// ErrNotQueued: only queued tickets dispatch (single-writer
	// discipline — draft tickets queue first; running tickets are already
	// dispatched).
	ErrNotQueued = errors.New("ticket is not queued")
	// ErrNoWorkflow: the ticket names no workflow definition.
	ErrNoWorkflow = errors.New("ticket has no workflow_name")
	// ErrWorkflowNotFound: the named definition (or pinned version) does
	// not exist in the workflow store.
	ErrWorkflowNotFound = errors.New("workflow definition not found")
	// ErrNotTicketDispatchable: the workflow's authored input signature cannot
	// carry a ticket. Ticket dispatch supplies exactly two immutable inputs —
	// one work-item/v1 and one repository/v1 — so a workflow that declares
	// neither, or several of either, is not something a ticket can run. This
	// is an inspectable client configuration error, so the route maps it
	// to 422.
	ErrNotTicketDispatchable = errors.New("workflow input signature cannot carry a ticket")
	// ErrBudgetExhausted: the binder's durable budget reservation — the
	// single admission authority — refused the run. The ticket STAYS
	// QUEUED; the loop logs it as deferred and the route maps it to 409.
	// Never a ticket-state transition: budgets recover (midnight, raised cap).
	ErrBudgetExhausted = errors.New("budget exhausted; dispatch deferred")
	// ErrInputsPending is a retryable adapter state: the ticket is durably
	// reserved, but an exact immutable input (normally the repository
	// snapshot selected by upload/resource capture/UI) is not available yet.
	ErrInputsPending = errors.New("ticket workflow inputs pending")
)

// WorkflowResolver is the subset of workflow.Store dispatch reads.
type WorkflowResolver interface {
	Live(name string) (*workflow.Definition, bool, error)
	Get(name string, version int) (*workflow.Definition, bool, error)
}

// WorkItemCapturer is satisfied by workitem.Capturer. Ticket dispatch owns
// the mutable-to-immutable boundary and never exposes ticket reads to the
// generic workflow compiler.
type WorkItemCapturer interface {
	CaptureRevision(context.Context, int) (workitem.CaptureResult, bool, error)
}

// WorkflowBinder is satisfied by workflowrun.Binder. Keeping the seam narrow
// makes the ticket path an adapter over the same admission path used by
// manual, scheduled, and experiment invocations. The binder is the authority
// on what may be admitted, including the schema-v3 gate.
type WorkflowBinder interface {
	BindAndCreate(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error)
}

// WorkflowRunCanceler compensates a completed generic admission when the
// ticket loses its durable reservation before the run can be linked. The
// concrete workflowrun.Canceler is idempotent and team-scoped.
type WorkflowRunCanceler interface {
	Cancel(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
}

// ticketPortMapping maps adapter meanings to authored workflow input names.
// Names are not reserved in schema v3; the ports are inferred by TYPE, which
// is the only rule — see resolveTicketPorts.
type ticketPortMapping struct {
	workItem   string
	repository string
}

type Deps struct {
	Tickets   tickets.Store
	Workflows WorkflowResolver

	// Ticket adapter dependencies. Team identity is server-derived; the
	// binder re-authorizes every selected snapshot for that team.
	TeamID           int
	TeamName         string
	WorkItems        WorkItemCapturer
	WorkflowBinder   WorkflowBinder
	WorkflowCanceler WorkflowRunCanceler
}

// Result is the outcome of one ticket dispatch. WorkflowRunID is the identity:
// it is what the ticket is durably linked to and what every downstream view
// keys on. PipelineRunID is an optional execution-linkage diagnostic.
type Result struct {
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id"`
	PipelineRunID *int                   `json:"pipeline_run_id,omitempty"`
	// Warnings carries advisory WorkItemTextLint findings on the ticket prose
	// (vocabulary known to trigger CLI usage-policy false refusals). Advisory
	// only — never a dispatch blocker.
	Warnings []string `json:"warnings,omitempty"`
}

// DispatchOne adapts a queued ticket to its selected workflow. It atomically
// reserves the ticket, captures its work-item revision, binds exact snapshot
// IDs through workflowrun.Binder, records both run identities, and only then
// moves queued→running through Transition. Every pre-transition operation is
// retry-safe under the durable reservation key; the generic binder owns
// template/run/secret/schema admission.
func DispatchOne(ctx context.Context, deps Deps, ticketID int, dispatchedBy string) (Result, error) {
	t, found, err := deps.Tickets.Get(ticketID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, tickets.ErrTicketNotFound
	}
	// A running ticket that still holds its reservation and every durable
	// identity is RESUMABLE: dispatch is idempotent under the reservation key,
	// so a re-entry finishes the interrupted attempt instead of refusing it.
	resumable := t.State == tickets.StateRunning && t.DispatchReservationKey != "" &&
		t.WorkflowRunID != nil && t.WorkItemSnapshotID != nil && t.RepositorySnapshotID != nil &&
		t.WorkflowVersion != nil && t.WorkflowDefinitionID != nil
	if t.State != tickets.StateQueued && !resumable {
		return Result{}, fmt.Errorf("%w (state: %s)", ErrNotQueued, t.State)
	}
	if t.WorkflowName == "" {
		return Result{}, ErrNoWorkflow
	}

	var def *workflow.Definition
	if t.WorkflowVersion != nil {
		def, found, err = deps.Workflows.Get(t.WorkflowName, *t.WorkflowVersion)
	} else {
		def, found, err = deps.Workflows.Live(t.WorkflowName)
	}
	if err != nil {
		return Result{}, err
	}
	if !found {
		version := "live"
		if t.WorkflowVersion != nil {
			version = fmt.Sprintf("v%d", *t.WorkflowVersion)
		}
		return Result{}, fmt.Errorf("%w: %s %s", ErrWorkflowNotFound, t.WorkflowName, version)
	}

	// Advisory work-item lint: warn — NEVER block — on prose the claude CLI's
	// usage-policy check has false-refused before. Only the ticket title and
	// body are linted: dispatch renders the ticket through its captured
	// work-item/v1 snapshot, which is exactly those two fields.
	warnings := WorkItemTextLint(t.Title, t.Body)

	res, err := dispatch(ctx, deps, t, def, dispatchedBy)
	if err != nil {
		return res, err
	}
	res.Warnings = warnings
	return res, nil
}

func dispatch(
	ctx context.Context,
	deps Deps,
	ticket *tickets.Ticket,
	definition *workflow.Definition,
	dispatchedBy string,
) (Result, error) {
	if deps.TeamID <= 0 || strings.TrimSpace(deps.TeamName) == "" || deps.WorkItems == nil ||
		deps.WorkflowBinder == nil || deps.WorkflowCanceler == nil {
		return Result{}, fmt.Errorf("ticket dispatch is not configured")
	}
	// Resolve the adapter's ports FIRST: it is a pure read of the authored
	// signature, so a workflow a ticket cannot run is refused before any
	// durable side effect.
	ports, err := resolveTicketPorts(*definition)
	if err != nil {
		return Result{}, err
	}

	reservation, err := deps.Tickets.ReserveDispatch(ctx, ticket.ID, tickets.DispatchReservationRequest{
		ExpectedRevision: ticket.Revision, WorkflowVersion: definition.Version, WorkflowDefinitionID: definition.ID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("reserve ticket dispatch: %w", err)
	}
	reserved := reservation.Ticket
	if reserved.RepositorySnapshotID == nil {
		return Result{}, ErrInputsPending
	}
	if err := reserved.RepositorySnapshotID.Validate(); err != nil {
		return Result{}, fmt.Errorf("%w: invalid repository snapshot selection", tickets.ErrDispatchConflict)
	}

	var workItemID snapshot.SnapshotID
	if reserved.WorkItemSnapshotID != nil {
		workItemID = *reserved.WorkItemSnapshotID
	} else {
		capture, found, err := deps.WorkItems.CaptureRevision(ctx, ticket.ID)
		if err != nil {
			return Result{}, fmt.Errorf("capture work-item snapshot: %w", err)
		}
		if !found {
			return Result{}, ErrInputsPending
		}
		if capture.TicketID != ticket.ID || capture.Revision <= 0 || capture.Snapshot.ID.Validate() != nil ||
			capture.Snapshot.Type != snapshot.TypeRef("work-item/v1") {
			return Result{}, fmt.Errorf("capture work-item snapshot: invalid capture result")
		}
		if err := deps.Tickets.RecordDispatchWorkItem(
			ctx, ticket.ID, reservation.Key, capture.Revision, capture.Snapshot.ID,
		); err != nil {
			return Result{}, fmt.Errorf("record work-item snapshot: %w", err)
		}
		workItemID = capture.Snapshot.ID
	}

	// Re-read the durable selection after capture. Repository selection may
	// fill a previously-pending reservation exactly once, but neither input
	// may be silently rebound after it is recorded.
	current, found, err := deps.Tickets.Get(ticket.ID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, tickets.ErrTicketNotFound
	}
	if current.DispatchReservationKey != reservation.Key || current.WorkItemSnapshotID == nil ||
		*current.WorkItemSnapshotID != workItemID || current.RepositorySnapshotID == nil {
		return Result{}, tickets.ErrDispatchConflict
	}
	repositoryID := *current.RepositorySnapshotID

	version := definition.Version
	bindResult, err := deps.WorkflowBinder.BindAndCreate(ctx, workflowrun.AdmissionContext{
		TeamID: deps.TeamID, TeamName: deps.TeamName, CreatedBy: dispatchedBy,
		Origin: workflowrun.Origin{Kind: workflowrun.OriginKindTicket, Reference: strconv.Itoa(ticket.ID)},
	}, workflowrun.BindRequest{
		WorkflowName: definition.Name, Version: &version,
		Inputs: map[string]snapshot.SnapshotID{
			ports.workItem: workItemID, ports.repository: repositoryID,
		},
		IdempotencyKey: reservation.Key,
	})
	if err != nil {
		switch {
		case errors.Is(err, workflowrun.ErrBudgetDenied):
			return Result{}, ErrBudgetExhausted
		case errors.Is(err, workflowrun.ErrSnapshotUnavailable), errors.Is(err, workflowrun.ErrSnapshotTypeMismatch),
			errors.Is(err, workflowrun.ErrInvalidRequest):
			// The selected values or authored signature cannot be admitted by
			// this adapter. This is inspectable input/configuration, not an ATC
			// platform fault; a human can unqueue/reset and select again.
			return Result{}, fmt.Errorf("%w: generic binder rejected ticket snapshots: %v", ErrNotTicketDispatchable, err)
		case errors.Is(err, workflowrun.ErrIdempotencyConflict):
			return Result{}, fmt.Errorf("%w: %v", tickets.ErrDispatchConflict, err)
		}
		return Result{}, err
	}
	// The binder already verified the run against the request it was given
	// (definition identity, version, schema, inputs); re-comparing here only
	// duplicated its authority. What this adapter still owns is the identity
	// it is about to write onto the ticket.
	run := bindResult.Run
	if run.ID.Validate() != nil {
		return Result{}, fmt.Errorf("%w: generic binder returned an invalid workflow-run identity", tickets.ErrDispatchConflict)
	}
	result := Result{WorkflowRunID: run.ID, PipelineRunID: cloneInt(run.PipelineRunID)}
	if run.PipelineRunID == nil || *run.PipelineRunID <= 0 {
		// A workflow run without execution linkage is legal (an admission that
		// never reached a pipeline run), but the ticket's durable link is
		// written as a pair, so there is nothing safe to record yet. The
		// reservation and its idempotency key survive, so the next pass
		// re-binds this exact run. The identity still rides on the Result.
		return result, fmt.Errorf(
			"workflow run %s has no execution linkage to record on ticket %d", run.ID, ticket.ID,
		)
	}
	if recordErr := deps.Tickets.RecordDispatchRun(ctx, ticket.ID, reservation.Key, run.ID, *run.PipelineRunID); recordErr != nil {
		latest, found, readErr := deps.Tickets.Get(ticket.ID)
		if readErr != nil {
			return result, errors.Join(fmt.Errorf("record workflow run: %w", recordErr), fmt.Errorf("re-read ticket ownership: %w", readErr))
		}
		if !found || !ticketOwnsReservation(latest, reservation.Key) {
			return result, cancelOrphanedWorkflowRun(ctx, deps, run.ID, fmt.Errorf("record workflow run: %w", recordErr))
		}
		if latest.WorkflowRunID == nil && latest.PipelineRunID == nil {
			// The write failed before committing. The reservation still owns the
			// binder idempotency key, so a retry will recover this exact run.
			return result, fmt.Errorf("record workflow run: %w", recordErr)
		}
		if !ticketLinksWorkflowRun(latest, run.ID, *run.PipelineRunID) {
			return result, cancelOrphanedWorkflowRun(ctx, deps, run.ID, fmt.Errorf("record workflow run: %w", recordErr))
		}
	}
	transitionErr := deps.Tickets.Transition(ticket.ID, tickets.StateQueued, tickets.StateRunning,
		tickets.TransitionMeta{PipelineRunID: run.PipelineRunID})
	if transitionErr != nil {
		latest, found, readErr := deps.Tickets.Get(ticket.ID)
		if readErr != nil {
			return result, errors.Join(fmt.Errorf("transition ticket to running: %w", transitionErr), fmt.Errorf("re-read ticket ownership: %w", readErr))
		}
		if !found || !ticketOwnsReservation(latest, reservation.Key) ||
			!ticketLinksWorkflowRun(latest, run.ID, *run.PipelineRunID) {
			return result, cancelOrphanedWorkflowRun(ctx, deps, run.ID,
				fmt.Errorf("workflow run %s created but ticket %d transition failed: %w", run.ID, ticket.ID, transitionErr))
		}
		if latest.State != tickets.StateRunning {
			// The exact run remains durably linked to the queued reservation.
			// A later idempotent dispatch will retry only the state transition.
			return result, fmt.Errorf("workflow run %s linked but ticket %d transition failed: %w", run.ID, ticket.ID, transitionErr)
		}
	}
	return result, nil
}

func ticketOwnsReservation(ticket *tickets.Ticket, reservationKey string) bool {
	if ticket == nil || ticket.DispatchReservationKey != reservationKey ||
		(ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
		return false
	}
	return true
}

func ticketLinksWorkflowRun(ticket *tickets.Ticket, workflowRunID snapshot.WorkflowRunID, pipelineRunID int) bool {
	return ticket.WorkflowRunID != nil && *ticket.WorkflowRunID == workflowRunID &&
		ticket.PipelineRunID != nil && *ticket.PipelineRunID == pipelineRunID
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cancelOrphanedWorkflowRun(
	ctx context.Context,
	deps Deps,
	runID snapshot.WorkflowRunID,
	cause error,
) error {
	// Compensation is deliberately detached from a canceled request: once
	// admission has created paid work, cleanup is a server responsibility.
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, found, err := deps.WorkflowCanceler.Cancel(cancelCtx, deps.TeamID, runID)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("cancel orphaned workflow run %s: %w", runID, err))
	}
	if !found {
		return fmt.Errorf("%w (workflow run %s no longer exists)", cause, runID)
	}
	return fmt.Errorf("%w (orphaned workflow run %s cancelled)", cause, runID)
}

// resolveTicketPorts infers the adapter's two ports from the authored input
// signature BY TYPE — the only rule. A ticket supplies exactly one
// work-item/v1 and one repository/v1 value, so the workflow must declare
// exactly one input of each type; anything else is a workflow a ticket cannot
// run, and the error says which way it is wrong.
func resolveTicketPorts(definition workflow.Definition) (ticketPortMapping, error) {
	signature, err := definition.Compiled.PublicSignature()
	if err != nil {
		return ticketPortMapping{}, fmt.Errorf("%w: read authored signature: %v", ErrNotTicketDispatchable, err)
	}
	var mapping ticketPortMapping
	workItems, repositories := 0, 0
	for _, port := range signature.Inputs {
		switch port.Type {
		case snapshot.TypeRef("work-item/v1"):
			workItems++
			mapping.workItem = port.Name
		case snapshot.TypeRef("repository/v1"):
			repositories++
			mapping.repository = port.Name
		}
	}
	if workItems != 1 || repositories != 1 {
		return ticketPortMapping{}, fmt.Errorf(
			"%w: workflow %s v%d declares %d work-item/v1 and %d repository/v1 inputs; ticket dispatch needs exactly one of each",
			ErrNotTicketDispatchable, definition.Name, definition.Version, workItems, repositories,
		)
	}
	return mapping, nil
}
