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
	// ErrWorkflowNotV3: ticket dispatch accepts only schema-v3 definitions.
	ErrWorkflowNotV3 = errors.New("workflow definition is not schema v3")
	// ErrRenderRefused: the schema-v3 workflow cannot be adapted to the
	// ticket's exact work-item/v1 and repository/v1 inputs. This is an
	// inspectable client configuration error, so the route maps it to 422.
	ErrRenderRefused = errors.New("workflow cannot be dispatched as a ticket")
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
// manual, scheduled, and experiment invocations.
type WorkflowBinder interface {
	BindAndCreate(context.Context, workflowrun.AdmissionContext, workflowrun.BindRequest) (workflowrun.BindResult, error)
}

// WorkflowRunCanceler compensates a completed generic admission when the
// ticket loses its durable reservation before the run can be linked. The
// concrete workflowrun.Canceler is idempotent and team-scoped.
type WorkflowRunCanceler interface {
	Cancel(context.Context, int, snapshot.WorkflowRunID) (db.AgentWorkflowRun, bool, error)
}

// TicketPortMapping maps adapter meanings to authored workflow input names.
// Names are not reserved in schema v3. The default resolver selects the
// unique exact work-item/v1 and repository/v1 ports by type.
type TicketPortMapping struct {
	WorkItem   string
	Repository string
}

type TicketPortResolver interface {
	ResolveTicketPorts(context.Context, workflow.Definition) (TicketPortMapping, error)
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
	TicketPorts      TicketPortResolver
}

type Result struct {
	RunID         int                     `json:"run_id"`
	PipelineName  string                  `json:"pipeline_name"`
	WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	// Warnings carries advisory SpecLint findings on the dispatched prose
	// (ticket #46: vocabulary known to trigger CLI usage-policy false
	// refusals). Advisory only — never a dispatch blocker.
	Warnings []string `json:"warnings,omitempty"`
}

// DispatchOne adapts a queued ticket to its selected schema-v3 workflow.
// It atomically reserves the ticket, captures its work-item revision, binds
// exact snapshot IDs through workflowrun.Binder, records both run identities,
// and only then moves queued→running through Transition. Every pre-transition
// operation is retry-safe under the durable
// reservation key; the generic binder owns template/run/secret admission.
func DispatchOne(ctx context.Context, deps Deps, ticketID int, dispatchedBy string) (Result, error) {
	t, found, err := deps.Tickets.Get(ticketID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, tickets.ErrTicketNotFound
	}
	resumeV3 := t.State == tickets.StateRunning && t.DispatchReservationKey != "" &&
		t.WorkflowRunID != nil && t.WorkItemSnapshotID != nil && t.RepositorySnapshotID != nil &&
		t.WorkflowVersion != nil && t.WorkflowDefinitionID != nil
	if t.State != tickets.StateQueued && !resumeV3 {
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
	if def.SchemaVersion != 3 {
		return Result{}, fmt.Errorf(
			"%w: workflow %s v%d uses schema_version %d",
			ErrWorkflowNotV3,
			def.Name,
			def.Version,
			def.SchemaVersion,
		)
	}

	// Advisory spec lint (ticket #46): warn — NEVER block — on prose the
	// claude CLI's usage-policy check has false-refused before. Only the
	// work-item title and body are linted here: schema-v3 dispatch renders
	// the ticket through its captured work-item/v1 snapshot and never reads
	// a separate spec record.
	warnings := SpecLint(t.Title, t.Body)

	res, err := dispatchV3(ctx, deps, t, def, dispatchedBy)
	if err != nil {
		return res, err
	}
	res.Warnings = warnings
	return res, nil
}

func dispatchV3(
	ctx context.Context,
	deps Deps,
	ticket *tickets.Ticket,
	definition *workflow.Definition,
	dispatchedBy string,
) (Result, error) {
	if deps.TeamID <= 0 || strings.TrimSpace(deps.TeamName) == "" || deps.WorkItems == nil ||
		deps.WorkflowBinder == nil || deps.WorkflowCanceler == nil {
		return Result{}, fmt.Errorf("schema-v3 ticket dispatch is not configured")
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

	ports, err := resolveTicketPorts(ctx, deps.TicketPorts, *definition)
	if err != nil {
		return Result{}, fmt.Errorf("%w: resolve schema-v3 ticket inputs: %v", ErrRenderRefused, err)
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
		Origin: workflowrun.Origin{Kind: "ticket", Reference: strconv.Itoa(ticket.ID)},
	}, workflowrun.BindRequest{
		WorkflowName: definition.Name, Version: &version,
		Inputs: map[string]snapshot.SnapshotID{
			ports.WorkItem: workItemID, ports.Repository: repositoryID,
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
			return Result{}, fmt.Errorf("%w: generic binder rejected ticket snapshots: %v", ErrRenderRefused, err)
		case errors.Is(err, workflowrun.ErrIdempotencyConflict):
			return Result{}, fmt.Errorf("%w: %v", tickets.ErrDispatchConflict, err)
		}
		return Result{}, err
	}
	run := bindResult.Run
	if run.ID.Validate() != nil || run.PipelineRunID == nil || *run.PipelineRunID <= 0 ||
		run.WorkflowDefinitionID != definition.ID || run.WorkflowName != definition.Name ||
		run.WorkflowVersion != definition.Version || run.SchemaVersion != 3 {
		return Result{}, fmt.Errorf("%w: generic binder returned different workflow identity", tickets.ErrDispatchConflict)
	}
	if recordErr := deps.Tickets.RecordDispatchRun(ctx, ticket.ID, reservation.Key, run.ID, *run.PipelineRunID); recordErr != nil {
		latest, found, readErr := deps.Tickets.Get(ticket.ID)
		if readErr != nil {
			return Result{}, errors.Join(fmt.Errorf("record workflow run: %w", recordErr), fmt.Errorf("re-read ticket ownership: %w", readErr))
		}
		if !found || !ticketOwnsReservation(latest, reservation.Key) {
			return Result{}, cancelOrphanedWorkflowRun(ctx, deps, run.ID, fmt.Errorf("record workflow run: %w", recordErr))
		}
		if latest.WorkflowRunID == nil && latest.PipelineRunID == nil {
			// The write failed before committing. The reservation still owns the
			// binder idempotency key, so a retry will recover this exact run.
			return Result{}, fmt.Errorf("record workflow run: %w", recordErr)
		}
		if !ticketLinksWorkflowRun(latest, run.ID, *run.PipelineRunID) {
			return Result{}, cancelOrphanedWorkflowRun(ctx, deps, run.ID, fmt.Errorf("record workflow run: %w", recordErr))
		}
	}
	transitionErr := deps.Tickets.Transition(ticket.ID, tickets.StateQueued, tickets.StateRunning,
		tickets.TransitionMeta{PipelineRunID: run.PipelineRunID})
	if transitionErr != nil {
		latest, found, readErr := deps.Tickets.Get(ticket.ID)
		if readErr != nil {
			return Result{}, errors.Join(fmt.Errorf("transition ticket to running: %w", transitionErr), fmt.Errorf("re-read ticket ownership: %w", readErr))
		}
		if !found || !ticketOwnsReservation(latest, reservation.Key) ||
			!ticketLinksWorkflowRun(latest, run.ID, *run.PipelineRunID) {
			return Result{}, cancelOrphanedWorkflowRun(ctx, deps, run.ID,
				fmt.Errorf("workflow run %s created but ticket %d transition failed: %w", run.ID, ticket.ID, transitionErr))
		}
		if latest.State != tickets.StateRunning {
			// The exact run remains durably linked to the queued reservation.
			// A later idempotent dispatch will retry only the state transition.
			return Result{}, fmt.Errorf("workflow run %s linked but ticket %d transition failed: %w", run.ID, ticket.ID, transitionErr)
		}
	}

	pipelineName, err := workflow.TemplateName(workflow.TargetWorkflow, definition.Name, definition.Version, "", run.ParameterizedConfigHash)
	if err != nil {
		return Result{}, fmt.Errorf("derive workflow template name: %w", err)
	}
	workflowRunID := run.ID
	return Result{RunID: *run.PipelineRunID, PipelineName: pipelineName, WorkflowRunID: &workflowRunID}, nil
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

func resolveTicketPorts(
	ctx context.Context,
	resolver TicketPortResolver,
	definition workflow.Definition,
) (TicketPortMapping, error) {
	var mapping TicketPortMapping
	var err error
	if resolver != nil {
		mapping, err = resolver.ResolveTicketPorts(ctx, definition)
		if err != nil {
			return TicketPortMapping{}, err
		}
	}
	signature, err := definition.Compiled.PublicSignature()
	if err != nil {
		return TicketPortMapping{}, err
	}
	if resolver == nil {
		for _, port := range signature.Inputs {
			switch port.Type {
			case snapshot.TypeRef("work-item/v1"):
				if mapping.WorkItem != "" {
					return TicketPortMapping{}, fmt.Errorf("multiple work-item/v1 inputs require explicit ticket port mapping")
				}
				mapping.WorkItem = port.Name
			case snapshot.TypeRef("repository/v1"):
				if mapping.Repository != "" {
					return TicketPortMapping{}, fmt.Errorf("multiple repository/v1 inputs require explicit ticket port mapping")
				}
				mapping.Repository = port.Name
			}
		}
	}
	if strings.TrimSpace(mapping.WorkItem) == "" || strings.TrimSpace(mapping.Repository) == "" || mapping.WorkItem == mapping.Repository {
		return TicketPortMapping{}, fmt.Errorf("ticket mapping requires distinct work-item and repository ports")
	}
	portTypes := make(map[string]snapshot.TypeRef, len(signature.Inputs))
	for _, port := range signature.Inputs {
		portTypes[port.Name] = port.Type
	}
	if portTypes[mapping.WorkItem] != snapshot.TypeRef("work-item/v1") ||
		portTypes[mapping.Repository] != snapshot.TypeRef("repository/v1") {
		return TicketPortMapping{}, fmt.Errorf("ticket port mapping does not match the authored workflow signature")
	}
	return mapping, nil
}
