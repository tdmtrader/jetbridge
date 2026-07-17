package dispatch

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/api/tickets"
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

	ATCExternalURL string
	RepoBaseURL    string
}

type Result struct {
	RunID        int    `json:"run_id"`
	PipelineName string `json:"pipeline_name"`
}

// DispatchOne is the dispatcher core: claim a QUEUED ticket, resolve
// and freeze its workflow definition, render the template pipeline,
// persist it as agent-ticket-<id>, create the pipeline run, and move
// the ticket queued→running through the single-writer Transition.
//
// v0 (manual-dispatch slice): invoked by the DispatchAgentTicket route
// only — a human pulling the trigger IS the budget gate while budget
// admission is deferred. Plan 11's Dispatcher loop later wraps exactly
// this function. Failures before the transition leave the ticket
// queued, so a retry is always safe.
func DispatchOne(deps Deps, ticketID int, dispatchedBy string) (Result, error) {
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
		return Result{}, fmt.Errorf("render workflow %s v%d for ticket %d: %w", def.Name, def.Version, ticketID, err)
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

	if err := deps.Tickets.Transition(ticketID, tickets.StateQueued, tickets.StateRunning,
		tickets.TransitionMeta{PipelineRunID: &runID}); err != nil {
		// The run exists but the ticket did not move (raced or store
		// failure). Surface it — the human trigger retries or reconciles.
		return Result{}, fmt.Errorf("run %d created but ticket %d transition failed: %w", runID, ticketID, err)
	}

	return Result{RunID: runID, PipelineName: pipelineName}, nil
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
