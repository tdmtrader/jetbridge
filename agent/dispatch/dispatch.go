package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/agent/api/principals"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
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
	// ErrBudgetExhausted: admission refused — the ticket or the global
	// daily cap has no headroom (§2.7). The ticket STAYS QUEUED; the
	// loop logs it as deferred and the route maps it to 409. Never a
	// ticket-state transition: budgets recover (midnight, raised cap).
	ErrBudgetExhausted = errors.New("budget exhausted; dispatch deferred")
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

type Deps struct {
	Tickets   tickets.Store
	Workflows WorkflowResolver
	Templates TemplateSaver
	Runs      RunCreator

	// Budget, when non-nil, gates admission per §2.7: TicketRemaining +
	// GlobalDailyRemaining are consulted BEFORE any side effect beyond
	// the version freeze. nil skips admission (tests; pre-budget wiring).
	Budget budget.Checker

	// The §8.2 run-credential leg: the exec mounts CLAUDE_CODE_OAUTH_TOKEN
	// exclusively from the agent-run-<id> secret on ticketed runs, so
	// dispatch must mint a per-run principal, resolve a vaulted Anthropic
	// credential, and Attach the secret before the step's pod starts.
	// Secrets == nil skips the leg (unit/DB tests without a cluster).
	Principals  principals.Store
	Credentials credentials.Backend

	// Users, when non-nil, resolves the ticket's triggering user id at
	// dispatch (the create handler records only the username). nil skips
	// (platform-funded, as before).
	Users   UserLookup
	Secrets credentials.SecretAttacher

	// RunTimeout bounds the per-run principal token (§2.8.2: expires_at =
	// now + --agent-run-timeout). Zero preserves the pre-flag 24h default.
	RunTimeout time.Duration

	// SecretLabels, when non-nil, adds the concourse/ticket label after a
	// successful Attach. Best-effort: failures are logged, never fatal.
	SecretLabels RunSecretLabeler

	ATCExternalURL string
	RepoBaseURL    string
}

type Result struct {
	RunID        int    `json:"run_id"`
	PipelineName string `json:"pipeline_name"`
	// Warnings: advisory SpecLint findings on the dispatched prose
	// (ticket #46). Never blocks — surfaced on the dispatch response and
	// at info by the dispatcher loop.
	Warnings []string `json:"warnings,omitempty"`
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

	if deps.Budget != nil {
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
	if def.SchemaVersion != 1 && def.SchemaVersion != 2 {
		return Result{}, fmt.Errorf("%w: workflow %s v%d uses schema_version %d; legacy ticket dispatch supports only schema versions 1 and 2", ErrRenderRefused, def.Name, def.Version, def.SchemaVersion)
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
			t.UserID = &uid
		}
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
		timeout := deps.RunTimeout
		if timeout <= 0 {
			timeout = 24 * time.Hour // pre-flag default, kept for zero-value Deps
		}
		expires := time.Now().Add(timeout).Unix()
		_, token, err := deps.Principals.Create(principals.CreateSpec{
			// §2.8.2 name — identical to the secret name so the reaper's
			// RevokeByName(RunSecretName(runID)) adapter finds it.
			Name:        credentials.RunSecretName(runID),
			Description: fmt.Sprintf("per-run principal for pipeline run %d (ticket %d)", runID, t.ID),
			Scopes: []string{
				principals.ScopeTicketsRead,
				principals.ScopeTicketsWrite,
				principals.ScopeMetricsWrite,
				principals.ScopeCostsWrite,
				principals.ScopeQuestionsAnswer, // wave-3 platform-mcp; additive now (tokens are immutable)
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
