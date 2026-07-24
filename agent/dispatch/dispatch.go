package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workitem"
	"github.com/concourse/concourse/atc"
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
	// ErrRenderRefused: the resolved workflow cannot be rendered in v0
	// (declared-but-unenforced policy blocks, sidecars, checkpoints, mcp
	// delivery). A client error — the workflow is malformed for v0, not a
	// server fault — so the route maps it to 422, not 500.
	ErrRenderRefused = errors.New("workflow cannot be dispatched in v0")
	// ErrBudgetExhausted: admission refused — the ticket or the global
	// daily cap has no headroom (§2.7). The ticket STAYS QUEUED; the
	// loop logs it as deferred and the route maps it to 409. Never a
	// ticket-state transition: budgets recover (midnight, raised cap).
	ErrBudgetExhausted = errors.New("budget exhausted; dispatch deferred")
	// ErrInputsPending is a retryable adapter state: the ticket is durably
	// reserved, but an exact immutable input (normally the repository
	// snapshot selected by upload/resource capture/UI) is not available yet.
	ErrInputsPending = errors.New("ticket workflow inputs pending")
)

// UserLookup resolves users.id from a username (db.NewAgentUserLookup).
type UserLookup interface {
	FindByUsername(username string) (int, bool, error)
}

// WorkflowResolver is the subset of workflow.Store dispatch reads.
type WorkflowResolver interface {
	Live(name string) (*workflow.Definition, bool, error)
	Get(name string, version int) (*workflow.Definition, bool, error)
}

// TemplateSaver persists a rendered template pipeline and returns its
// pipeline id. The db-backed implementation targets the main team and
// handles create-vs-update versioning (see NewTeamTemplateSaver).
type TemplateSaver interface {
	SaveTemplate(name string, cfg atc.Config) (pipelineID int, err error)
}

// RunCreator is the subset of db.PipelineRunFactory dispatch calls.
type RunCreator interface {
	CreateRun(templatePipelineID int, params map[string]any, createdBy string) (db.PipelineRun, error)
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
	Templates TemplateSaver
	Runs      RunCreator

	// Schema-v3 ticket adapter dependencies. Team identity is server-derived;
	// the binder re-authorizes every selected snapshot for that team.
	TeamID           int
	TeamName         string
	WorkItems        WorkItemCapturer
	WorkflowBinder   WorkflowBinder
	WorkflowCanceler WorkflowRunCanceler
	TicketPorts      TicketPortResolver

	// Budget, when non-nil, gates admission per §2.7: TicketRemaining +
	// GlobalDailyRemaining are consulted BEFORE any side effect beyond
	// the version freeze. nil skips admission (tests; pre-budget wiring).
	Budget budget.Checker

	// The §8.2 run-credential leg: the exec mounts CLAUDE_CODE_OAUTH_TOKEN
	// exclusively from the agent-run-<id> secret on ticketed runs, so
	// dispatch resolves a vaulted Anthropic credential and attaches the
	// secret before the step's pod starts.
	// Secrets == nil skips the leg (unit/DB tests without a cluster).
	Credentials credentials.Backend

	// Users, when non-nil, resolves the ticket's triggering user id at
	// dispatch (the create handler records only the username). nil skips
	// (platform-funded, as before).
	Users   UserLookup
	Secrets credentials.SecretAttacher

	// SecretLabels, when non-nil, adds the concourse/ticket label after a
	// successful Attach. Best-effort: failures are logged, never fatal.
	SecretLabels RunSecretLabeler

	ATCExternalURL string
	RepoBaseURL    string
}

type Result struct {
	RunID         int                     `json:"run_id"`
	PipelineName  string                  `json:"pipeline_name"`
	WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	// Warnings: advisory SpecLint findings on the dispatched prose
	// (ticket #46). Never blocks — surfaced on the dispatch response and
	// at info by the dispatcher loop.
	Warnings []string `json:"warnings,omitempty"`
}

// DispatchOne adapts a queued ticket to its selected workflow generation.
// Schema v1/v2 retain the compatibility renderer and per-ticket template.
// Schema v3 atomically reserves the ticket, captures its work-item revision,
// binds exact snapshot IDs through workflowrun.Binder, records both run
// identities, and only then moves queued→running through Transition.
// Every pre-transition v3 operation is retry-safe under the durable
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

	if t.State == tickets.StateQueued && deps.Budget != nil {
		tr, err := deps.Budget.TicketRemaining(ticketID)
		if err != nil {
			return Result{}, fmt.Errorf("budget admission for ticket %d: %w", ticketID, err)
		}
		if tr.Exhausted {
			return Result{}, fmt.Errorf("%w: ticket %d spent $%.2f of $%.2f", ErrBudgetExhausted, ticketID, tr.SpentUSD, tr.LimitUSD)
		}
		gr, err := deps.Budget.GlobalDailyRemaining()
		if err != nil {
			return Result{}, fmt.Errorf("budget admission (global daily): %w", err)
		}
		if gr.Exhausted {
			return Result{}, fmt.Errorf("%w: global daily cap spent $%.2f of $%.2f", ErrBudgetExhausted, gr.SpentUSD, gr.LimitUSD)
		}
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
	if resumeV3 && def.SchemaVersion != 3 {
		return Result{}, fmt.Errorf("%w (state: %s)", ErrNotQueued, t.State)
	}

	// Freeze the resolved version onto the ticket (contracts §1.7:
	// workflow_version NULL = live at dispatch time — dispatch resolves
	// and pins). Never state; Update is the non-state writer.
	if def.SchemaVersion != 3 && t.WorkflowVersion == nil {
		v := def.Version
		if err := deps.Tickets.Update(ticketID, tickets.Update{WorkflowVersion: &v}); err != nil {
			return Result{}, fmt.Errorf("freeze workflow version: %w", err)
		}
	}

	// Resolve the triggering user's id (the wave-4 leg ticket-core left to
	// dispatch): user-first credential funding and spend attribution key
	// on agent_tickets.user_id, which nothing populated before this.
	// Unresolvable username → platform-funded (found=false is not an
	// error); store faults ARE errors (ticket stays queued, retried).
	if t.UserID == nil && t.UserName != "" && deps.Users != nil {
		uid, found, err := deps.Users.FindByUsername(t.UserName)
		if err != nil {
			return Result{}, fmt.Errorf("resolve user %q: %w", t.UserName, err)
		}
		if found {
			if err := deps.Tickets.Update(ticketID, tickets.Update{UserID: &uid}); err != nil {
				return Result{}, fmt.Errorf("record user id: %w", err)
			}
			refreshed, found, err := deps.Tickets.Get(ticketID)
			if err != nil {
				return Result{}, fmt.Errorf("reload ticket after user resolution: %w", err)
			}
			if !found {
				return Result{}, tickets.ErrTicketNotFound
			}
			t = refreshed
		}
	}

	if def.SchemaVersion == 3 {
		return dispatchV3(ctx, deps, t, def, dispatchedBy)
	}
	if def.SchemaVersion != 1 && def.SchemaVersion != 2 {
		return Result{}, fmt.Errorf("%w: workflow %s v%d uses unsupported schema_version %d", ErrRenderRefused, def.Name, def.Version, def.SchemaVersion)
	}

	spec, _, err := deps.Tickets.LatestSpec(ticketID)
	if err != nil {
		return Result{}, err
	}

	// Advisory spec lint (ticket #46): warn — NEVER block — on prose the
	// claude CLI's usage-policy check has false-refused before. Both the
	// ticket body and the latest spec reach the agent (RenderSpecMarkdown),
	// so both are linted; duplicate findings collapse.
	warnings := SpecLint(t.Title, t.Body)
	if spec != nil {
		for _, w := range SpecLint(spec.Title, spec.Body) {
			seen := false
			for _, have := range warnings {
				if have == w {
					seen = true
					break
				}
			}
			if !seen {
				warnings = append(warnings, w)
			}
		}
	}

	planTasks, err := deps.Tickets.ActivePlan(ticketID)
	if err != nil {
		return Result{}, err
	}

	cfg, err := RenderLegacyTicket(RenderInput{
		Workflow:        def.Config,
		WorkflowName:    def.Name,
		WorkflowVersion: def.Version,
		WorkflowHash:    def.ContentHash,
		Ticket:          *t,
		Spec:            spec,
		PlanTasks:       planTasks,
		ATCExternalURL:  deps.ATCExternalURL,
		RepoBaseURL:     deps.RepoBaseURL,
	})
	if err != nil {
		return Result{}, fmt.Errorf("%w: render %s v%d for ticket %d: %s", ErrRenderRefused, def.Name, def.Version, ticketID, err)
	}

	pipelineName := fmt.Sprintf("agent-ticket-%d", ticketID)
	pipelineID, err := deps.Templates.SaveTemplate(pipelineName, cfg)
	if err != nil {
		return Result{}, fmt.Errorf("save template pipeline %s: %w", pipelineName, err)
	}

	run, err := deps.Runs.CreateRun(pipelineID, nil, dispatchedBy)
	if err != nil {
		return Result{}, fmt.Errorf("create pipeline run: %w", err)
	}
	runID := run.ID()

	if deps.Secrets != nil {
		if err := attachRunSecret(ctx, deps, t, runID); err != nil {
			return Result{}, fmt.Errorf("attach run %d credential secret: %w", runID, err)
		}
	}

	if err := deps.Tickets.Transition(ticketID, tickets.StateQueued, tickets.StateRunning,
		tickets.TransitionMeta{PipelineRunID: &runID}); err != nil {
		// The run exists but the ticket did not move (raced or store
		// failure). Surface it — the human trigger retries or reconciles.
		return Result{}, fmt.Errorf("run %d created but ticket %d transition failed: %w", runID, ticketID, err)
	}

	return Result{RunID: runID, PipelineName: pipelineName, Warnings: warnings}, nil
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
			errors.Is(err, workflowrun.ErrInvalidRequest), errors.Is(err, workflowrun.ErrLegacyDefinition):
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

// attachRunSecret creates the §8.2 agent-run-<runID> secret: a vaulted
// Anthropic credential (the ticket's triggering user when resolvable,
// else the §1.13 platform service user — the same funding path
// PlatformSecretSyncer maintains).
func attachRunSecret(ctx context.Context, deps Deps, t *tickets.Ticket, runID int) error {
	cred, err := resolveRunCredential(deps, t)
	if err != nil {
		return err
	}

	if _, err := deps.Secrets.Attach(ctx, runID, cred); err != nil {
		return err
	}

	if deps.SecretLabels != nil && t.ID > 0 {
		if err := deps.SecretLabels.Label(ctx, runID, t.ID); err != nil {
			// Best-effort by contract (§2.8.2): operator filtering only.
			lagerctx.FromContext(ctx).Session("attach-run-secret").Error("failed-to-label-run-secret", err,
				lager.Data{"run": runID, "ticket": t.ID})
		}
	}
	return nil
}

// resolveRunCredential picks the vaulted Anthropic credential funding
// this run: the ticket's triggering user's row when user_id is resolved
// (full user resolution is the Dispatcher loop's job), falling back to
// the §1.13 platform service user. OAuth wins over API key, matching
// PlatformSecretSyncer.
func resolveRunCredential(deps Deps, t *tickets.Ticket) (*credentials.Credential, error) {
	kinds := []string{credentials.KindAnthropicOAuth, credentials.KindAnthropicAPIKey}
	now := time.Now().Unix()
	var expiredOwners []string

	usable := func(cred *credentials.Credential) bool {
		if cred.ExpiresAt > 0 && cred.ExpiresAt <= now {
			owner := cred.UserName
			if owner == "" {
				owner = fmt.Sprintf("user %d", cred.UserID)
			}
			expiredOwners = append(expiredOwners, fmt.Sprintf("%s (%s, expired %s)",
				owner, cred.Kind, time.Unix(cred.ExpiresAt, 0).UTC().Format(time.RFC3339)))
			return false
		}
		return true
	}

	if t.UserID != nil {
		for _, kind := range kinds {
			cred, found, err := deps.Credentials.Resolve(*t.UserID, kind)
			if err != nil {
				return nil, err
			}
			if found && usable(cred) {
				return cred, nil
			}
		}
	}

	platformID, _, found, err := deps.Credentials.UserBySub(credentials.PlatformUserSub)
	if err != nil {
		return nil, err
	}
	if found {
		for _, kind := range kinds {
			cred, credFound, err := deps.Credentials.Resolve(platformID, kind)
			if err != nil {
				return nil, err
			}
			if credFound && usable(cred) {
				return cred, nil
			}
		}
	}

	if len(expiredOwners) > 0 {
		return nil, fmt.Errorf("no usable Anthropic credential for ticket %d — expired: %s; re-vault with `fly agent auth` (or `--platform`)",
			t.ID, strings.Join(expiredOwners, "; "))
	}
	return nil, fmt.Errorf("no vaulted Anthropic credential for ticket %d (user or platform): run `fly agent auth` or `fly agent auth --platform`", t.ID)
}

// NewTeamTemplateSaver returns the db-backed TemplateSaver: templates
// live on the main team; an existing pipeline is updated at its current
// config version (re-dispatch after send_back re-renders in place) and
// always left unpaused.
func NewTeamTemplateSaver(teamFactory db.TeamFactory, teamName string) TemplateSaver {
	return &teamTemplateSaver{teamFactory: teamFactory, teamName: teamName}
}

type teamTemplateSaver struct {
	teamFactory db.TeamFactory
	teamName    string
}

func (s *teamTemplateSaver) SaveTemplate(name string, cfg atc.Config) (int, error) {
	team, found, err := s.teamFactory.FindTeam(s.teamName)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, fmt.Errorf("team %s not found", s.teamName)
	}

	ref := atc.PipelineRef{Name: name}
	from := db.ConfigVersion(0)
	if existing, found, err := team.Pipeline(ref); err != nil {
		return 0, err
	} else if found {
		from = existing.ConfigVersion()
	}

	pipeline, _, err := team.SavePipeline(ref, cfg, from, false)
	if err != nil {
		return 0, err
	}
	return pipeline.ID(), nil
}
