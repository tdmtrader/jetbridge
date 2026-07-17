package dispatch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/workflow"
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
)

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

type Deps struct {
	Tickets   tickets.Store
	Workflows WorkflowResolver
	Templates TemplateSaver
	Runs      RunCreator

	// The §8.2 run-credential leg: the exec mounts CLAUDE_CODE_OAUTH_TOKEN
	// exclusively from the agent-run-<id> secret on ticketed runs, so
	// dispatch must mint a per-run principal, resolve a vaulted Anthropic
	// credential, and Attach the secret before the step's pod starts.
	// Secrets == nil skips the leg (unit/DB tests without a cluster).
	Principals  principals.Store
	Credentials credentials.Backend
	Secrets     credentials.SecretAttacher

	ATCExternalURL string
	RepoBaseURL    string
}

type Result struct {
	RunID        int    `json:"run_id"`
	PipelineName string `json:"pipeline_name"`
}

// DispatchOne is the dispatcher core: claim a QUEUED ticket, resolve
// and freeze its workflow definition, render the template pipeline,
// persist it as agent-ticket-<id>, create the pipeline run, attach the
// agent-run-<id> credential secret, and move the ticket queued→running
// through the single-writer Transition.
//
// v0 (manual-dispatch slice): invoked by the DispatchAgentTicket route
// only — a human pulling the trigger IS the budget gate while budget
// admission is deferred. Plan 11's Dispatcher loop later wraps exactly
// this function. Failures before the transition leave the ticket
// queued, so a retry is always safe (Attach is idempotent per run id;
// an orphaned run from a failed attempt just errors harmlessly).
func DispatchOne(ctx context.Context, deps Deps, ticketID int, dispatchedBy string) (Result, error) {
	t, found, err := deps.Tickets.Get(ticketID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, tickets.ErrTicketNotFound
	}
	if t.State != tickets.StateQueued {
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

	// Freeze the resolved version onto the ticket (contracts §1.7:
	// workflow_version NULL = live at dispatch time — dispatch resolves
	// and pins). Never state; Update is the non-state writer.
	if t.WorkflowVersion == nil {
		v := def.Version
		if err := deps.Tickets.Update(ticketID, tickets.Update{WorkflowVersion: &v}); err != nil {
			return Result{}, fmt.Errorf("freeze workflow version: %w", err)
		}
	}

	spec, _, err := deps.Tickets.LatestSpec(ticketID)
	if err != nil {
		return Result{}, err
	}
	planTasks, err := deps.Tickets.ActivePlan(ticketID)
	if err != nil {
		return Result{}, err
	}

	cfg, err := Render(RenderInput{
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

	return Result{RunID: runID, PipelineName: pipelineName}, nil
}

// attachRunSecret creates the §8.2 agent-run-<runID> secret: a vaulted
// Anthropic credential (the ticket's triggering user when resolvable,
// else the §1.13 platform service user — the same funding path
// PlatformSecretSyncer maintains) plus a freshly-minted per-run
// principal token (name run-<id>, 24h expiry; consumed by the
// platform/gateway sidecars once wave 3 lands, inert until then).
func attachRunSecret(ctx context.Context, deps Deps, t *tickets.Ticket, runID int) error {
	cred, err := resolveRunCredential(deps, t)
	if err != nil {
		return err
	}

	principalToken := ""
	if deps.Principals != nil {
		expires := time.Now().Add(24 * time.Hour).Unix()
		_, token, err := deps.Principals.Create(principals.CreateSpec{
			Name:        fmt.Sprintf("run-%d", runID),
			Description: fmt.Sprintf("per-run principal for pipeline run %d (ticket %d)", runID, t.ID),
			Scopes: []string{
				principals.ScopeTicketsRead,
				principals.ScopeTicketsWrite,
				principals.ScopeMetricsWrite,
				principals.ScopeCostsWrite,
			},
			CreatedBy: "dispatch",
			ExpiresAt: &expires,
		})
		if err != nil {
			return fmt.Errorf("mint run principal: %w", err)
		}
		principalToken = token
	}

	if _, err := deps.Secrets.Attach(ctx, runID, cred, principalToken); err != nil {
		return err
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

	if t.UserID != nil {
		for _, kind := range kinds {
			cred, found, err := deps.Credentials.Resolve(*t.UserID, kind)
			if err != nil {
				return nil, err
			}
			if found {
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
			if credFound {
				return cred, nil
			}
		}
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
