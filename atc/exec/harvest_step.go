package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/harvest"
	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const harvestProcessID = "harvest"

const harvestFlightArtifact = "flight"

type HarvestStepOption func(*HarvestStep)

// WithHarvestTicketsStore wires the single-writer ticket store so the
// exec can transition running→needs_review/errored after the runner
// exits (contracts §2.1 — harvest is the primary needs_review writer).
func WithHarvestTicketsStore(s tickets.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.ticketsStore = s }
}

// WithHarvestRunVerifier wires the same server-side linkage gate the
// agent step uses: a plan-carried ticket id transitions ONLY when the
// (verified) pipeline run was dispatched for it.
// WithHarvestPlatformTokenSecret names the long-lived §8.2 platform
// credential secret (key anthropic-token) funding the judge. The judge
// NEVER uses the per-run agent-run-<id> user token — precedence is the
// OPPOSITE of agent steps (§2.8.1/§8.3).
func WithHarvestPlatformTokenSecret(name string) HarvestStepOption {
	return func(h *HarvestStep) { h.platformTokenSecret = name }
}

func WithHarvestRunVerifier(v AgentRunVerifier) HarvestStepOption {
	return func(h *HarvestStep) { h.runVerifier = v }
}

// WithHarvestStreamer wires the artifact streamer used to pull the
// pod-written flight output (results.json / events.ndjson / review.json)
// server-side after the runner exits (Task 12 ingestion).
func WithHarvestStreamer(s Streamer) HarvestStepOption {
	return func(h *HarvestStep) { h.streamer = s }
}

// WithHarvestMetricsStore sets the store used for server-side flight
// ingestion (agent_run_metrics rows). Nil disables ingestion.
func WithHarvestMetricsStore(m metrics.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.metricsStore = m }
}

// WithHarvestReviewsStore sets the store the judged review.json evidence
// is upserted into (agent_reviews). Unlike CI reviews — which arrive via
// the principal-authenticated HTTP path — harvest evidence is pulled off
// the pod-written flight volume, so every trust-bearing column is pinned
// server-side (see ingestAndRecord; plan D9).
func WithHarvestReviewsStore(r reviews.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.reviewsStore = r }
}

// WithHarvestOutcomesStore wires delivery-outcomes' store so a
// successful push seeds the agent_outcomes row with the runner's
// authoritative shas (§1.11.1: harvest is the primary outcome-row
// creator; the outcome watcher's branch-head fallback covers rows
// harvest could not seed). nil disables seeding — never fatal.
func WithHarvestOutcomesStore(s outcomes.Store) HarvestStepOption {
	return func(h *HarvestStep) { h.outcomesStore = s }
}

// WithHarvestBudgetRecorder sets the budget library used for the
// fire-and-forget harvest_judge ledger append.
func WithHarvestBudgetRecorder(c budget.Checker) HarvestStepOption {
	return func(h *HarvestStep) { h.budgetChecker = c }
}

// WithHarvestPlatformUserResolver supplies the §1.13 platform-user
// lookup (UserBySub("agent-platform")) so the harvest_judge ledger row
// carries platform attribution. A nil resolver logs-and-skips
// attribution, never fails the step.
func WithHarvestPlatformUserResolver(r PlatformUserResolver) HarvestStepOption {
	return func(h *HarvestStep) { h.platformUserResolver = r }
}

// PlatformUserResolver is the UserBySub subset the §1.13 ledger
// attribution needs (matches agent/credentials.Backend /
// atc/db.AgentUserCredentialsFactory).
type PlatformUserResolver interface {
	UserBySub(sub string) (userID int, userName string, found bool, err error)
}

// HarvestStep is the terminal delivery step (contracts §2.8.1), v0:
// verify the committed workspace and push the stable ticket branch via
// harvest-runner, then transition the ticket. Gates and the judge are
// the full harvest-step workstream; plans declaring them are refused
// here, never silently skipped.
type HarvestStep struct {
	planID              atc.PlanID
	plan                atc.HarvestPlan
	metadata            StepMetadata
	containerMetadata   db.ContainerMetadata
	workerPool          Pool
	delegateFactory     TaskDelegateFactory
	defaultTimeout      time.Duration
	agentImage          string
	ticketsStore        tickets.Store
	platformTokenSecret string
	runVerifier         AgentRunVerifier

	// Task 12 ingestion seams — all optional, nil-guarded.
	streamer             Streamer
	metricsStore         metrics.Store
	reviewsStore         reviews.Store
	outcomesStore        outcomes.Store
	budgetChecker        budget.Checker
	platformUserResolver PlatformUserResolver
}

func NewHarvestStep(
	planID atc.PlanID,
	plan atc.HarvestPlan,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	delegateFactory TaskDelegateFactory,
	defaultTimeout time.Duration,
	agentImage string,
	opts ...HarvestStepOption,
) Step {
	s := &HarvestStep{
		planID:            planID,
		plan:              plan,
		metadata:          metadata,
		containerMetadata: containerMetadata,
		workerPool:        workerPool,
		delegateFactory:   delegateFactory,
		defaultTimeout:    defaultTimeout,
		agentImage:        agentImage,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (step *HarvestStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "harvest", tracing.Attrs{
		"name": step.plan.Name,
	})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "harvest", step.plan.Name, time.Since(start))

	return ok, err
}

func (step *HarvestStep) run(ctx context.Context, state RunState, delegate TaskDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = tracing.LoggerWithSpan(ctx, logger)
	logger = logger.Session("harvest-step", lager.Data{
		"step-name": step.plan.Name,
		"job-id":    step.metadata.JobID,
	})

	oteltrace.SpanFromContext(ctx).AddEvent("step.initializing")
	delegate.Initializing(logger)

	if step.agentImage == "" {
		return false, errors.New("harvest step requires the web node to be started with --agent-step-image")
	}

	// v0.5 refusals: fail closed on config the runner cannot enforce (the
	// dogfood ticket #5 pattern — never a silent skip). Gates are now
	// admitted when every gate's scope is "full" (the v0.5 in-pod fixed
	// command map, enforced by harvest-runner before push); any other
	// scope still needs dev-mcp (full harvest-step workstream, wave 3)
	// and is refused here rather than surfacing mid-run.
	for _, g := range step.plan.GatePolicy.Gates {
		if g.Scope != "full" {
			return false, fmt.Errorf("harvest gate %q has scope %q: v0.5 only enforces scope \"full\" in-pod — remove the gate or wait for dev-mcp (full harvest-step workstream, wave 3)", g.Gate, g.Scope)
		}
	}
	if step.plan.Judge != nil {
		if err := step.plan.Judge.Validate(); err != nil {
			return false, fmt.Errorf("harvest judge config invalid: %w", err)
		}
		if step.platformTokenSecret == "" {
			return false, errors.New("harvest judge requires the web node to be started with --agent-platform-token-secret (the §8.2 platform credential; the judge never uses per-run user tokens)")
		}
	}
	if step.plan.DevMCP != nil {
		return false, errors.New("harvest v0.5 runs no dev-mcp-backed gates, so dev_mcp has no consumer: remove it")
	}
	if step.plan.Push && step.plan.Branch == "" {
		return false, errors.New("harvest push requires a branch")
	}

	// Env is static-only, same rule as agent steps (§2.8): values are
	// render/dispatch-time literals; anything still carrying a var ref
	// was never resolved and must fail closed.
	planEnv := make(map[string]string, len(step.plan.Env))
	for k, v := range step.plan.Env {
		if refs := vars.ExtractVarRefs(v); len(refs) > 0 {
			return false, fmt.Errorf("harvest env %s contains unresolved var reference ((%s))", k, refs[0].String())
		}
		planEnv[k] = v
	}

	// F30 run-id fallback: the renderer cannot place ((run_id)) in the
	// int pipeline_run_id field, so identity travels as env.
	runID := step.plan.PipelineRunID
	if runID == 0 {
		runID, _ = strconv.Atoi(planEnv["AGENT_PIPELINE_RUN_ID"])
	}

	workdir := step.containerMetadata.WorkingDirectory

	cfg := harvest.Config{
		StepName:      step.plan.Name,
		Workspace:     step.plan.Workspace,
		Repo:          step.plan.Repo,
		TargetBranch:  step.plan.TargetBranch,
		TicketID:      step.plan.TicketID,
		PipelineRunID: runID,
		Branch:        step.plan.Branch,
		Push:          step.plan.Push,
		GatePolicy:    step.plan.GatePolicy,
		Judge:         step.plan.Judge,
	}
	cfgPayload, err := json.Marshal(cfg)
	if err != nil {
		return false, err
	}

	env := step.metadata.TaskEnv()
	for k, v := range planEnv {
		env = append(env, k+"="+v)
	}
	env = append(env,
		"HARVEST_CONFIG="+string(cfgPayload),
		"HARVEST_WORKSPACE_DIR="+artifactPath(workdir, step.plan.Workspace, ""),
		"AGENT_FLIGHT_DIR="+artifactPath(workdir, harvestFlightArtifact, ""),
		"AGENT_PLAN_ID="+string(step.planID),
	)

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,
		StepName: step.plan.Name,

		ImageSpec: runtime.ImageSpec{ImageURL: step.agentImage},
		Env:       env,
		Type:      step.containerMetadata.Type,

		Dir: workdir,

		// The flight dir rides an output artifact so evidence survives the
		// pod (Task 12 ingests it via the artifact streamer).
		Outputs: runtime.OutputPaths{harvestFlightArtifact: ensureTrailingSlash(artifactPath(workdir, harvestFlightArtifact, ""))},
	}

	if step.plan.Push {
		// §8.3: the per-repo git credential mounts read-only on the main
		// container only; sidecars (none in v0) can never see it.
		containerSpec.SecretMounts = []runtime.SecretMount{{
			SecretName: harvest.GitCredSecretName(step.plan.Repo),
			MountPath:  "/var/run/agent/git",
		}}
		containerSpec.Env = append(containerSpec.Env, "HARVEST_CREDS_DIR=/var/run/agent/git")
	}

	if step.plan.Judge != nil {
		// §8.2/§8.3: PLATFORM credential only, secretKeyRef-only —
		// jetbridge applySecretRefs APPENDS the EnvVar (F20, landed).
		containerSpec.SecretEnv = map[string]vars.SecretRef{
			"CLAUDE_CODE_OAUTH_TOKEN": {Name: step.platformTokenSecret, Key: "anthropic-token"},
		}
	}

	repository := state.ArtifactRepository()
	artifact, fromCache, found := repository.ArtifactFor(build.ArtifactName(step.plan.Workspace))
	if !found {
		return false, MissingInputsError{[]string{step.plan.Workspace}}
	}
	containerSpec.Inputs = []runtime.Input{{
		Artifact:        artifact,
		DestinationPath: artifactPath(workdir, step.plan.Workspace, ""),
		FromCache:       fromCache,
	}}

	tracing.Inject(ctx, &containerSpec)

	owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)

	if err := delegate.BeforeSelectWorker(logger); err != nil {
		return false, err
	}
	chosenWorker, err := step.workerPool.FindOrSelectWorker(
		ctx, owner, containerSpec, worker.Spec{TeamID: step.metadata.TeamID},
	)
	if err != nil {
		return false, err
	}

	ctx, cancel, err := MaybeTimeout(ctx, step.plan.Timeout, step.defaultTimeout)
	if err != nil {
		return false, err
	}
	defer cancel()
	ctx = lagerctx.NewContext(ctx, logger)

	delegate.SelectedWorker(logger, chosenWorker.Name())

	container, volumeMounts, err := chosenWorker.FindOrCreateContainer(ctx, owner, step.containerMetadata, containerSpec, delegate)
	if err != nil {
		return false, err
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.starting")
	delegate.Starting(logger)
	processStart := time.Now()

	processSpec := runtime.ProcessSpec{
		ID:   harvestProcessID,
		Path: "harvest-runner",
		Dir:  workdir,
	}
	process, err := attachOrRun(ctx, container, processSpec, runtime.ProcessIO{
		Stdout: delegate.Stdout(),
		Stderr: delegate.Stderr(),
	})
	if err != nil {
		return false, err
	}

	result, runErr := process.Wait(ctx)

	// Synchronous server-side ingestion of the flight recorder — on EVERY
	// path where the container ran, including DeadlineExceeded and process
	// failures, BEFORE exit handling returns (mirrors AgentStep). ctx here
	// is the timeout-scoped context; ingestAndRecord detaches from it
	// internally (finding F4). The validated results document (nil when
	// missing/malformed) feeds the exit-0 outcome seeding below.
	results := step.ingestAndRecord(ctx, logger, chosenWorker, volumeMounts, time.Since(processStart))

	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			step.transitionTicket(ctx, logger, "errored", "", "harvest timed out")
			delegate.Errored(logger, TimeoutLogMessage)
			return false, nil
		}
		// Process-level failures (pod died, exec stream broke, binary
		// missing) never reach the exit-taxonomy switch — without this
		// the ticket silently stays `running` with a dead run (live
		// finding, ticket #12's first harvest attempt).
		step.transitionTicket(ctx, logger, "errored", "", "harvest process failed: "+runErr.Error())
		return false, runErr
	}

	// §2.8.1 exit taxonomy → ticket lifecycle (§2.1): 0/1 land in
	// needs_review (branch recorded only on ok+push), 2 errors the ticket.
	switch result.ExitStatus {
	case 0:
		branch := ""
		if step.plan.Push {
			branch = step.plan.Branch
		}
		step.transitionTicket(ctx, logger, "needs_review", branch, "")
		if branch != "" {
			// AFTER the transition — that ordering is load-bearing
			// (§1.11.1: the row must never precede the lifecycle edge).
			step.seedOutcome(logger, results, branch)
		}
	case 1:
		step.transitionTicket(ctx, logger, "needs_review", "", "")
	default:
		step.transitionTicket(ctx, logger, "errored", "", fmt.Sprintf("harvest platform error (exit %d) — see the harvest step log", result.ExitStatus))
	}

	delegate.Finished(logger, ExitStatus(result.ExitStatus))
	return result.ExitStatus == 0, nil
}

// verifiedIDs returns the plan-carried (ticketID, runID) only when the
// server-side verifier confirms the claims; (0, 0) otherwise. Plan env
// is attacker-writable YAML — a raw claim reaching LedgerEntry.TicketID
// would let any pipeline drain a victim ticket's budget with spend it
// never made (the agent-step 2026-07-11 review lesson). transitionTicket
// and ingestAndRecord both consume it, so linkage NEVER comes from raw
// plan env.
func (step *HarvestStep) verifiedIDs(logger lager.Logger) (int, int) {
	ticketID := step.plan.TicketID
	if ticketID <= 0 {
		return 0, 0
	}
	runID := step.plan.PipelineRunID
	if runID == 0 {
		runID, _ = strconv.Atoi(step.plan.Env["AGENT_PIPELINE_RUN_ID"])
	}

	if step.runVerifier == nil {
		logger.Info("skipping-agent-linkage", lager.Data{"reason": "no run verifier wired"})
		return 0, 0
	}
	owned, err := step.runVerifier.RunBelongsToPipeline(runID, step.metadata.PipelineID)
	if err != nil || !owned {
		logger.Info("skipping-agent-linkage", lager.Data{"reason": "run not verified for this pipeline", "run-id": runID})
		return 0, 0
	}
	linked, err := step.runVerifier.TicketBelongsToRun(ticketID, runID)
	if err != nil || !linked {
		logger.Info("skipping-agent-linkage", lager.Data{"reason": "ticket not dispatched as this run", "ticket-id": ticketID, "run-id": runID})
		return 0, 0
	}
	return ticketID, runID
}

// transitionTicket moves the plan's ticket through the single writer,
// guarded exactly like the agent step's admission: the plan-carried
// ticket id counts only when the server-verified run was dispatched for
// it (plan env is attacker-writable YAML). Stale/racing transitions are
// benign — the run-completion reconciler and manual close-out are
// legitimate concurrent writers.
func (step *HarvestStep) transitionTicket(ctx context.Context, logger lager.Logger, to string, branch, errorDetail string) {
	if step.ticketsStore == nil {
		return
	}
	ticketID, _ := step.verifiedIDs(logger)
	if ticketID <= 0 {
		return
	}

	meta := tickets.TransitionMeta{Branch: branch, ErrorDetail: errorDetail}
	err := step.ticketsStore.Transition(ticketID, tickets.StateRunning, tickets.State(to), meta)
	switch {
	case err == nil:
		logger.Info("ticket-transitioned", lager.Data{"ticket-id": ticketID, "to": to, "branch": branch})
	case errors.Is(err, tickets.ErrStaleTransition), errors.Is(err, tickets.ErrTicketNotFound):
		// benign: a concurrent writer (reconciler, manual close-out) won
		logger.Info("ticket-transition-superseded", lager.Data{"ticket-id": ticketID, "to": to, "error": err.Error()})
	default:
		logger.Error("ticket-transition-failed", err, lager.Data{"ticket-id": ticketID, "to": to})
	}
}

// seedOutcome creates/refreshes the agent_outcomes row with the runner's
// authoritative shas at push time (§1.11.1 primary path; the outcome
// watcher's branch-head fallback covers rows harvest could not seed).
// It runs AFTER transitionTicket — transition-first ordering is
// load-bearing — and only for server-verified ticket claims (plan env is
// attacker-writable YAML). res is the run-report ingestAndRecord parsed
// off the flight volume; nil (missing/malformed results.json) still
// seeds the row — it anchors created_at — just with empty shas.
// Failures are logged, never fatal, never affect the step result.
func (step *HarvestStep) seedOutcome(logger lager.Logger, res *schema.Results, branch string) {
	if step.outcomesStore == nil {
		return
	}
	ticketID, _ := step.verifiedIDs(logger)
	if ticketID == 0 {
		// never seed on an unverified claim
		return
	}
	var pushed, base string
	if res != nil {
		// schema.Results.Metadata is a map (judge-evidence Task 9) — the
		// shas are map keys, not struct fields.
		pushed = metaString(res.Metadata, "head_sha")
		base = metaString(res.Metadata, "base_sha")
	}
	err := step.outcomesStore.Ensure(&outcomes.Outcome{
		TicketID:  ticketID,
		Repo:      step.plan.Repo,
		Branch:    branch,
		PushedSha: pushed,
		BaseSha:   base,
	})
	if err != nil {
		logger.Error("failed-to-seed-outcome", err, lager.Data{"ticket-id": ticketID})
		return
	}
	logger.Info("outcome-seeded", lager.Data{"ticket-id": ticketID, "pushed-sha": pushed, "base-sha": base})
}

// metaString pulls a string value out of schema.Results' interface-typed
// metadata map ("" when absent or not a string).
func metaString(m map[string]interface{}, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// ingestAndRecord reads flight/results.json, flight/events.ndjson and
// flight/review.json from the registered flight output and records an
// agent_run_metrics row, an agent_reviews evidence row, and a
// harvest_judge cost-ledger append. It runs synchronously before Run
// returns, on every path where the container ran (including timeouts).
// It is tolerant by design: missing/partial/corrupt input degrades to a
// status=error metrics row; its own failures are logged, never returned.
// It returns the validated results document (nil when it never parsed)
// so the exit-0 push path can seed the outcome row with the runner's
// authoritative shas (Task B4) without a second flight read.
//
// Trust boundary (plan D9): unlike agent reviews — which reach the DB
// through the principal-authenticated HTTP path — the review.json here
// is pulled off the pod-written flight volume, which is attacker-writable
// (claude runs in the pod as root with AGENT_FLIGHT_DIR in its env). So
// every trust-bearing column is pinned server-side: Repo from step.plan,
// TicketID/PipelineRunID from verifiedIDs, SubmittedBy "harvest". The
// payload's own repo/ticket fields are advisory display data only.
func (step *HarvestStep) ingestAndRecord(
	ctx context.Context,
	logger lager.Logger,
	wkr runtime.Worker,
	volumeMounts []runtime.VolumeMount,
	wallTime time.Duration,
) *schema.Results {
	if step.metricsStore == nil {
		return nil
	}

	// parsedResults is the validated run-report threaded back to the
	// caller for outcome seeding; everything below is byte-identical
	// ingestion behavior.
	var parsedResults *schema.Results

	ticketID, runID := step.verifiedIDs(logger)

	// Detach from the step deadline (finding F4): on the DeadlineExceeded
	// path — the costliest, most measurement-critical case — ctx is already
	// cancelled and every StreamFile read threaded through it would fail
	// instantly. WithoutCancel keeps request-scoped values (tracing, lager)
	// while dropping the deadline; the fresh 30s bound keeps ingestion
	// (which blocks the build from completing) from hanging on a wedged
	// fabric.
	ingestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	rm := schema.RunMetrics{
		BuildID:         step.metadata.BuildID,
		PlanID:          string(step.planID),
		StepName:        step.plan.Name,
		WallTimeSeconds: int(wallTime.Seconds()),
	}
	// ticketID/runID are the SERVER-VERIFIED identities — never raw plan
	// env. When the verifier rejects the claim, both stay nil but the rows
	// are still written (spec 7): the measurement is real even when the
	// linkage is not provable.
	if ticketID > 0 {
		rm.TicketID = &ticketID
	}
	if runID > 0 {
		rm.PipelineRunID = &runID
	}
	rm.WorkflowName = step.plan.Env["AGENT_WORKFLOW_NAME"]
	if v, ok := envInt(step.plan.Env, "AGENT_WORKFLOW_VERSION"); ok {
		rm.WorkflowVersion = &v
	}
	rm.WorkflowHash = step.plan.Env["AGENT_WORKFLOW_HASH"]

	flightPath := ensureTrailingSlash(artifactPath(step.containerMetadata.WorkingDirectory, harvestFlightArtifact, ""))
	var flightArtifact runtime.Artifact
	for _, mount := range volumeMounts {
		if filepath.Clean(mount.MountPath) == filepath.Clean(flightPath) {
			flightArtifact = wkr.ArtifactFromVolume(mount.Volume)
			rm.EventsArtifact = mount.Volume.Handle()
		}
	}

	rm.Status = schema.RunStatusError
	rm.Summary = "flight recorder output missing"

	// flightRead tracks whether ANY flight data was actually read. When it
	// stays false this is a DEGRADED ingestion (missing mount, ephemeral
	// locator on a restart-resume, reaped pod) and the metrics write below
	// must go through InsertIfAbsent so it can never clobber a real row
	// (finding F24).
	flightRead := false
	var reviewRaw []byte

	if flightArtifact != nil && step.streamer != nil {
		// results.json
		rc, err := step.streamer.StreamFile(ingestCtx, flightArtifact, "results.json")
		if err != nil {
			logger.Error("failed-to-stream-flight-file", err, lager.Data{"file": "results.json"})
		} else if rc != nil {
			flightRead = true
			raw, readErr := io.ReadAll(io.LimitReader(rc, 5<<20))
			rc.Close()
			var results schema.Results
			if readErr == nil && json.Unmarshal(raw, &results) == nil && results.Validate() == nil {
				parsedResults = &results
				status, abstained := schema.ThreeWayStatus(results.Status)
				rm.Status = status
				rm.Summary = results.Summary
				rm.Results = json.RawMessage(raw)
				if abstained {
					logger.Info("harvest-abstained")
				}
			} else {
				rm.Summary = "results.json missing or malformed"
			}
		}

		// events.ndjson: counts + judge-only cost rollup + step.end wall time
		rc, err = step.streamer.StreamFile(ingestCtx, flightArtifact, "events.ndjson")
		if err != nil {
			logger.Error("failed-to-stream-flight-file", err, lager.Data{"file": "events.ndjson"})
		} else if rc != nil {
			flightRead = true
			counts := map[string]int{}
			sawStepEnd := false
			reader := schema.NewEventReader(rc)
			for {
				event, err := reader.Read()
				if err != nil {
					if !errors.Is(err, io.EOF) {
						logger.Error("flight-events-read-truncated", err)
					}
					break
				}
				counts[string(event.Type)]++
				switch event.Type {
				case schema.EventCostRecord:
					var c schema.CostRecordData
					// Only the judge's spend is the harvest step's own cost
					// (§1.13): gates and push are compute, not LLM spend, and
					// any foreign-source cost.record (contract §5 invites
					// other producers) belongs to its own ledger writer.
					if json.Unmarshal(event.Data, &c) == nil && c.Source == budget.SourceHarvestJudge {
						rm.Usage.InputTokens += c.InputTokens
						rm.Usage.OutputTokens += c.OutputTokens
						rm.Usage.CacheReadInputTokens += c.CacheReadTokens
						rm.Usage.CacheCreationInputTokens += c.CacheCreationTokens
						rm.Turns += c.Turns
						rm.CostUSD += c.CostUSD
						if c.Model != "" {
							rm.Model = c.Model
						}
					}
				case schema.EventStepEnd:
					sawStepEnd = true
					var e schema.StepEndData
					if json.Unmarshal(event.Data, &e) == nil && e.WallTimeSeconds > 0 {
						rm.WallTimeSeconds = e.WallTimeSeconds
					}
				}
			}
			rc.Close()
			if n := reader.Skipped(); n > 0 {
				logger.Info("flight-events-skipped-oversized-lines", lager.Data{"count": n})
			}
			rm.EventCounts = counts
			if !sawStepEnd && rm.Status != schema.RunStatusParked {
				// crashed runner: a stream missing step.end is defined as
				// error (shared-contracts §5 ingestion rule).
				rm.Status = schema.RunStatusError
				if rm.Summary == "" || rm.Summary == "flight recorder output missing" {
					rm.Summary = "event stream ended without step.end"
				}
			}
		}

		// review.json: the judged evidence document (Task 9 shape)
		rc, err = step.streamer.StreamFile(ingestCtx, flightArtifact, "review.json")
		if err != nil {
			logger.Error("failed-to-stream-flight-file", err, lager.Data{"file": "review.json"})
		} else if rc != nil {
			flightRead = true
			raw, readErr := io.ReadAll(io.LimitReader(rc, 5<<20))
			rc.Close()
			if readErr == nil {
				reviewRaw = raw
			} else {
				logger.Error("failed-to-read-flight-file", readErr, lager.Data{"file": "review.json"})
			}
		}
	}

	if !flightRead {
		// No flight file was read at all — the step produced no flight output
		// (dominant cause: a runner image predating the flight recorder). This
		// is a missing RECORDING, not a failed step: record it as incomplete so
		// DeriveOutcome renders it amber "unrecorded" on a succeeded build
		// (never red), while a failed/errored build still wins on read.
		rm.Status = schema.RunStatusIncomplete
		rm.Summary = "no flight output (runner image predates flight recorder?)"
	}

	// Evidence upsert — server-side trust pins (D9): Repo from the PLAN
	// (never the pod-written payload), TicketID/PipelineRunID verified-or-
	// nil, SubmittedBy "harvest". The payload supplies display columns only.
	if step.reviewsStore != nil && len(reviewRaw) > 0 {
		var payload reviews.ReviewPayload
		if err := json.Unmarshal(reviewRaw, &payload); err != nil {
			logger.Error("failed-to-parse-harvest-evidence", err)
		} else {
			rec := &reviews.StoredReview{
				BuildID:          step.metadata.BuildID,
				BuildName:        step.metadata.BuildName,
				TeamName:         step.metadata.TeamName,
				PipelineName:     step.metadata.PipelineName,
				JobName:          step.metadata.JobName,
				Repo:             step.plan.Repo,
				CommitSha:        payload.Metadata.Commit,
				Branch:           payload.Metadata.Branch,
				Score:            payload.Score.Value,
				MaxScore:         payload.Score.Max,
				Pass:             payload.Score.Pass,
				ProvenCount:      len(payload.ProvenIssues),
				ObservationCount: len(payload.Observations),
				Summary:          payload.Summary,
				AgentModel:       payload.Metadata.AgentModel,
				DurationSeconds:  payload.Metadata.DurationSec,
				Review:           json.RawMessage(reviewRaw),
				SubmittedBy:      "harvest",
			}
			if ticketID > 0 {
				rec.TicketID = &ticketID
			}
			if runID > 0 {
				rec.PipelineRunID = &runID
			}
			if err := step.reviewsStore.Upsert(rec); err != nil {
				logger.Error("failed-to-upsert-harvest-evidence", err)
			}
		}
	}

	// Metrics upsert + ledger dedup discipline — mirror of
	// AgentStep.ingestFlightRecorder (finding F3, amended for the
	// severed-exec case; see the long comment there): a fresh insert
	// charges rm's full cost; an update charges the DELTA against the
	// replaced row's counters; inserted=false with prev=nil is
	// indeterminate and skips the append, preserving "every dollar enters
	// the ledger exactly once". DEGRADED ingestion (flightRead=false) goes
	// through InsertIfAbsent so a zero-cost error shell can never clobber
	// a real row (finding F24).
	var inserted bool
	var prev *schema.RunMetrics
	var err error
	if flightRead {
		inserted, prev, err = step.metricsStore.UpsertReturningInserted(&rm)
	} else {
		inserted, err = step.metricsStore.InsertIfAbsent(&rm)
	}
	if err != nil {
		logger.Error("failed-to-ingest-run-metrics", err)
	}

	var ledgerCost float64
	var prevBase schema.RunMetrics
	if inserted {
		ledgerCost = rm.CostUSD
	} else if prev != nil {
		ledgerCost = rm.CostUSD - prev.CostUSD
		prevBase = *prev
	}

	if step.budgetChecker != nil && ledgerCost > 0 {
		entry := budget.LedgerEntry{
			TicketID:            rm.TicketID,
			PipelineRunID:       rm.PipelineRunID,
			BuildID:             rm.BuildID,
			StepName:            rm.StepName,
			Source:              budget.SourceHarvestJudge,
			Provider:            "anthropic",
			Model:               rm.Model,
			InputTokens:         max(rm.Usage.InputTokens-prevBase.Usage.InputTokens, 0),
			OutputTokens:        max(rm.Usage.OutputTokens-prevBase.Usage.OutputTokens, 0),
			CacheReadTokens:     max(rm.Usage.CacheReadInputTokens-prevBase.Usage.CacheReadInputTokens, 0),
			CacheCreationTokens: max(rm.Usage.CacheCreationInputTokens-prevBase.Usage.CacheCreationInputTokens, 0),
			Turns:               max(rm.Turns-prevBase.Turns, 0),
			CostUSD:             ledgerCost,
		}
		// §1.13 (FROZEN): harvest_judge spend is platform-initiated — the
		// row MUST carry the platform service user (sub 'agent-platform',
		// user_name 'platform'). A missing resolver/row skips attribution
		// loudly, never the append and never the step.
		if step.platformUserResolver == nil {
			logger.Info("harvest-judge-ledger-unattributed", lager.Data{"reason": "no platform user resolver wired"})
		} else if userID, userName, found, resErr := step.platformUserResolver.UserBySub(credentials.PlatformUserSub); resErr != nil || !found {
			logger.Info("harvest-judge-ledger-unattributed", lager.Data{"sub": credentials.PlatformUserSub, "found": found})
		} else {
			entry.UserID = &userID
			entry.UserName = userName
		}
		// Workflow attribution rides metadata->>'workflow' (shared-contracts
		// §4.2 addendum: writers that know their workflow MUST set it).
		if rm.WorkflowName != "" {
			version := 0
			if rm.WorkflowVersion != nil {
				version = *rm.WorkflowVersion
			}
			if md, mdErr := json.Marshal(map[string]string{
				"workflow": fmt.Sprintf("%s@%d", rm.WorkflowName, version),
			}); mdErr == nil {
				entry.Metadata = md
			}
		}
		if err := step.budgetChecker.Record(entry); err != nil {
			logger.Error("failed-to-record-cost-ledger", err) // fire-and-forget
		}
	}

	return parsedResults
}
