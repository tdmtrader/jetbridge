package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const agentProcessID = "agent"

// agentFlightArtifact is the implicit output every agent step produces: the
// flight recorder directory (results.json + events.ndjson), ingested
// server-side before the step returns (shared-contracts §1.8, plan 07 Task 1
// addendum).
const agentFlightArtifact = "flight"

// mcpSidecarPorts maps well-known sidecar names to their fixed localhost
// ports (shared-contracts §8.1).
var mcpSidecarPorts = map[string]int{"dev": 7780, "platform": 7781, "gateway": 7782}

// AgentRunVerifier proves, server-side, that the ticket/run identity claimed
// by plan env actually belongs to this build. AGENT_PIPELINE_RUN_ID and
// AGENT_TICKET_ID arrive via plan env (F30), which is public pipeline YAML:
// without these checks any team's pipeline could claim another run's
// `agent-run-<id>` secret (§8.2) and exfiltrate its principal and Anthropic
// tokens through a sidecar named "platform"/"gateway", or claim another
// ticket's id to admit against — and misattribute this step's spend into —
// a budget it does not own.
//
//counterfeiter:generate . AgentRunVerifier
type AgentRunVerifier interface {
	// RunBelongsToPipeline reports whether pipeline_runs row `runID` was
	// materialized as pipeline instance `pipelineID`.
	RunBelongsToPipeline(runID, pipelineID int) (bool, error)
	// TicketBelongsToRun reports whether agent_tickets row `ticketID` is
	// currently dispatched as pipeline run `runID`
	// (agent_tickets.pipeline_run_id, contracts §1.7).
	TicketBelongsToRun(ticketID, runID int) (bool, error)
}

// AgentStepOption configures optional fields on an AgentStep.
type AgentStepOption func(*AgentStep)

// WithAgentImageResolver sets an image resolver for sidecar digest pinning.
func WithAgentImageResolver(r imageresolver.Resolver) AgentStepOption {
	return func(s *AgentStep) { s.imageResolver = r }
}

// WithAgentMetricsStore sets the store used for server-side flight-recorder
// ingestion (agent_run_metrics rows).
func WithAgentMetricsStore(m metrics.Store) AgentStepOption {
	return func(s *AgentStep) { s.metricsStore = m }
}

// WithAgentBudgetChecker sets the checker used to resolve the step's budget
// slice against the ticket's remaining budget before the container starts.
func WithAgentBudgetChecker(c budget.Checker) AgentStepOption {
	return func(s *AgentStep) { s.budgetChecker = c }
}

// WithAgentRunVerifier sets the verifier consulted before any sidecar secret
// refs are injected. Without it the step fails closed: no refs are ever set.
func WithAgentRunVerifier(v AgentRunVerifier) AgentStepOption {
	return func(s *AgentStep) { s.runVerifier = v }
}

// AgentStep runs the claude CLI (via the agent-runner entrypoint) in a
// jetbridge pod with declared MCP sidecars, then ingests the flight
// recorder server-side (shared-contracts §2.8, §5, §8.1).
type AgentStep struct {
	planID            atc.PlanID
	plan              atc.AgentPlan
	defaultLimits     atc.ContainerLimits
	defaultRequests   atc.ContainerLimits
	metadata          StepMetadata
	containerMetadata db.ContainerMetadata
	workerPool        Pool
	streamer          Streamer
	delegateFactory   TaskDelegateFactory
	defaultTimeout    time.Duration
	agentImage        string
	imageResolver     imageresolver.Resolver
	metricsStore      metrics.Store
	budgetChecker     budget.Checker
	runVerifier       AgentRunVerifier
}

func NewAgentStep(
	planID atc.PlanID,
	plan atc.AgentPlan,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	streamer Streamer,
	delegateFactory TaskDelegateFactory,
	defaultTimeout time.Duration,
	agentImage string,
	opts ...AgentStepOption,
) Step {
	s := &AgentStep{
		planID:            planID,
		plan:              plan,
		defaultLimits:     defaultLimits,
		defaultRequests:   defaultRequests,
		metadata:          metadata,
		containerMetadata: containerMetadata,
		workerPool:        workerPool,
		streamer:          streamer,
		delegateFactory:   delegateFactory,
		defaultTimeout:    defaultTimeout,
		agentImage:        agentImage,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (step *AgentStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "agent", tracing.Attrs{
		"name": step.plan.Name,
	})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "agent", step.plan.Name, time.Since(start))

	return ok, err
}

func (step *AgentStep) run(ctx context.Context, state RunState, delegate TaskDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = tracing.LoggerWithSpan(ctx, logger)
	logger = logger.Session("agent-step", lager.Data{
		"step-name": step.plan.Name,
		"job-id":    step.metadata.JobID,
	})

	oteltrace.SpanFromContext(ctx).AddEvent("step.initializing")
	delegate.Initializing(logger)

	if step.agentImage == "" {
		return false, errors.New("agent step requires the web node to be started with --agent-step-image")
	}

	// Interpolate the plan's static env through the build's var sources.
	// Keys are walked in sorted order so the assembled env is deterministic.
	envKeys := make([]string, 0, len(step.plan.Env))
	for k := range step.plan.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	resolvedEnv := make(map[string]string, len(envKeys))
	for _, k := range envKeys {
		value, err := creds.NewString(state, step.plan.Env[k]).Evaluate()
		if err != nil {
			return false, fmt.Errorf("interpolate env %q: %w", k, err)
		}
		resolvedEnv[k] = value
	}

	// ---- Server-verified ticket/run identity ----
	//
	// AGENT_PIPELINE_RUN_ID and AGENT_TICKET_ID are carried by plan env
	// (F30) — attacker-writable pipeline YAML — so neither is EVER trusted
	// raw. Dispatch bakes ((run_id)) only into the run's own materialized
	// instance config, so a claimed run is legitimate exactly when its
	// pipeline_runs.instance_pipeline_id IS this build's pipeline; a claimed
	// ticket is legitimate exactly when that verified run was dispatched for
	// it (agent_tickets.pipeline_run_id, §1.7). Only VERIFIED values reach
	// budget admission, the agent-run-<id> secret name (§8.2), metrics
	// tagging and the cost ledger. An unverifiable claim fails the step
	// closed before any pod exists — otherwise any pipeline could claim
	// someone else's ticket id to admit against the victim's remaining
	// budget and misattribute this step's spend into their ledger, or claim
	// another run's id to mount its credentials.
	runID, _ := strconv.Atoi(resolvedEnv["AGENT_PIPELINE_RUN_ID"])
	verifiedRunID := 0
	if runID > 0 {
		if step.runVerifier == nil {
			logger.Info("skipping-run-secret-refs", lager.Data{
				"reason": "no agent run verifier wired; plan env cannot be trusted to name the run secret",
				"run-id": runID,
			})
		} else {
			owned, err := step.runVerifier.RunBelongsToPipeline(runID, step.metadata.PipelineID)
			if err != nil {
				return false, fmt.Errorf("verify pipeline run %d ownership: %w", runID, err)
			}
			if !owned {
				return false, fmt.Errorf("refusing to attach agent-run-%d credentials: pipeline run %d does not belong to this build's pipeline", runID, runID)
			}
			verifiedRunID = runID
		}
	}

	ticketID := 0
	if v := resolvedEnv["AGENT_TICKET_ID"]; v != "" {
		claimed, err := strconv.Atoi(v)
		if err != nil || claimed <= 0 {
			return false, fmt.Errorf("malformed AGENT_TICKET_ID %q: must be a positive integer (or empty for a pure-CI agent step)", v)
		}
		switch {
		case step.runVerifier == nil:
			// Fail closed like the secret refs above: the claim is dropped —
			// no ticket admission, NULL ticket attribution — so an unverified
			// value can never reach someone else's budget or ledger.
			logger.Info("skipping-ticket-identity", lager.Data{
				"reason":    "no agent run verifier wired; plan env cannot be trusted to claim a ticket",
				"ticket-id": claimed,
			})
		case verifiedRunID == 0:
			return false, fmt.Errorf("refusing to admit against ticket %d: AGENT_TICKET_ID requires a verified AGENT_PIPELINE_RUN_ID linking the ticket to this build", claimed)
		default:
			linked, err := step.runVerifier.TicketBelongsToRun(claimed, verifiedRunID)
			if err != nil {
				return false, fmt.Errorf("verify ticket %d linkage to pipeline run %d: %w", claimed, verifiedRunID, err)
			}
			if !linked {
				return false, fmt.Errorf("refusing to admit against ticket %d: not dispatched as pipeline run %d", claimed, verifiedRunID)
			}
			ticketID = claimed
		}
	}

	// Resolve the budget slice against the ticket's remaining budget. This
	// is a resolution (min of slice and ticket remaining), NOT a
	// reservation, and it runs at the start of EVERY execution of the step
	// — a continuation build re-resolves naturally and, because any
	// park-exit partial spend was already ledgered, the re-resolved slice
	// is automatically tighter (PARK-V2 seam delta §D, decision 32). The
	// ticket id is the SERVER-VERIFIED one from above, never raw plan env.
	slice := step.plan.BudgetSliceUSD
	if step.budgetChecker != nil && slice > 0 && ticketID > 0 {
		remaining, err := step.budgetChecker.StepSlice(ticketID, slice)
		if err != nil {
			logger.Error("failed-to-resolve-budget-slice", err)
			// keep the configured slice
		} else if remaining.Exhausted {
			delegate.Errored(logger, "budget slice exhausted before start")
			delegate.Finished(logger, ExitStatus(1))
			return false, nil
		} else {
			slice = remaining.RemainingUSD
		}
	}

	workdir := step.containerMetadata.WorkingDirectory

	// §8.1 main-container env contract.
	env := step.metadata.TaskEnv()
	for _, k := range envKeys {
		env = append(env, k+"="+resolvedEnv[k])
	}
	env = append(env, "AGENT_STEP_NAME="+step.plan.Name)
	if step.plan.Model != "" {
		env = append(env, "AGENT_MODEL="+step.plan.Model)
	}
	if step.plan.MaxTurns > 0 {
		env = append(env, "AGENT_MAX_TURNS="+strconv.Itoa(step.plan.MaxTurns))
	}
	if step.plan.OutputSchema != "" {
		env = append(env, "AGENT_OUTPUT_SCHEMA="+step.plan.OutputSchema)
	}
	if step.plan.Prompt != "" {
		env = append(env, "AGENT_PROMPT="+step.plan.Prompt)
	} else if step.plan.PromptFile != "" {
		env = append(env, "AGENT_PROMPT_FILE="+step.plan.PromptFile)
	}
	env = append(env, "AGENT_FLIGHT_DIR="+artifactPath(workdir, agentFlightArtifact, ""))
	if slice > 0 {
		env = append(env, "AGENT_BUDGET_SLICE_USD="+strconv.FormatFloat(slice, 'f', 2, 64))
	}

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,
		StepName: step.plan.Name,

		ImageSpec: runtime.ImageSpec{ImageURL: step.agentImage},
		Env:       env,
		Type:      step.containerMetadata.Type,

		Dir: workdir,
	}

	repository := state.ArtifactRepository()

	var missingInputs []string
	for _, name := range step.plan.Inputs {
		artifact, fromCache, found := repository.ArtifactFor(build.ArtifactName(name))
		if !found {
			missingInputs = append(missingInputs, name)
			continue
		}
		containerSpec.Inputs = append(containerSpec.Inputs, runtime.Input{
			Artifact:        artifact,
			DestinationPath: artifactPath(workdir, name, ""),
			FromCache:       fromCache,
		})
	}
	if len(missingInputs) > 0 {
		return false, MissingInputsError{missingInputs}
	}

	// Declared outputs plus the implicit flight recorder output.
	outputNames := step.outputNames()
	containerSpec.Outputs = make(runtime.OutputPaths, len(outputNames))
	for _, name := range outputNames {
		containerSpec.Outputs[name] = ensureTrailingSlash(artifactPath(workdir, name, ""))
	}

	limits := mergeContainerLimits(step.plan.Limits, step.defaultLimits)
	containerSpec.Limits.CPU = (*uint64)(limits.CPU)
	containerSpec.Limits.Memory = (*uint64)(limits.Memory)
	containerSpec.Limits.EphemeralStorage = (*uint64)(limits.EphemeralStorage)

	requests := mergeContainerLimits(step.plan.Requests, step.defaultRequests)
	containerSpec.Limits.CPURequest = (*uint64)(requests.CPU)
	containerSpec.Limits.MemoryRequest = (*uint64)(requests.Memory)
	containerSpec.Limits.EphemeralStorageRequest = (*uint64)(requests.EphemeralStorage)

	// For env values that were resolved from a K8s Secrets backend, record
	// the secret ref so the pod builder emits ValueFrom.SecretKeyRef instead
	// of a literal (§8.2) — this is how CLAUDE_CODE_OAUTH_TOKEN from a
	// K8s-secret var source stays out of the pod spec.
	containerSpec.SecretEnv = BuildSecretEnv(atc.TaskEnv(resolvedEnv), state)

	sidecars, err := loadSidecarConfigs(ctx, logger, repository, step.streamer, step.plan.Sidecars)
	if err != nil {
		return false, err
	}

	err = resolveSidecarImages(ctx, logger, state, step.imageResolver, sidecars)
	if err != nil {
		return false, err
	}

	// Emit sidecar plan events so the UI can render nested sidecar steps.
	if len(sidecars) > 0 {
		delegate.EmitSidecarPlans(logger, sidecars)
	}

	// MCP URL derivation is strictly by well-known sidecar name; other
	// names get no URL (§8.1). Done after loading so file-sourced sidecars
	// count too.
	for _, sc := range sidecars {
		if port, ok := mcpSidecarPorts[sc.Name]; ok {
			containerSpec.Env = append(containerSpec.Env,
				strings.ToUpper(sc.Name)+"_MCP_URL=http://127.0.0.1:"+strconv.Itoa(port)+"/mcp")
		}
	}

	// §8.1 sidecar rows (F15): common + identity rows for every MCP sidecar.
	// Identity rows are empty for pure-CI steps (no ticket/run env).
	common := []string{
		"ATC_EXTERNAL_URL=" + step.metadata.ExternalURL,
		"BUILD_ID=" + strconv.Itoa(step.metadata.BuildID),
	}
	if v := resolvedEnv["AGENT_TICKET_ID"]; v != "" {
		common = append(common, "AGENT_TICKET_ID="+v)
	}
	if v := resolvedEnv["AGENT_PIPELINE_RUN_ID"]; v != "" {
		common = append(common, "AGENT_PIPELINE_RUN_ID="+v)
	}

	// Main-container and sidecar secret refs derive from the deterministic
	// §8.2 secret name. No pipeline-run id ⇒ no run-secret refs at all
	// (pure-CI agent steps route the token through a K8s-secret var source
	// via BuildSecretEnv above, per §8.1). secretRunID is the SERVER-VERIFIED
	// run id from the identity block above: it stays 0 — and no refs are
	// injected — unless the ownership check passed.
	secretRunID := verifiedRunID
	secretName := "agent-run-" + strconv.Itoa(secretRunID)

	// §8.1 pins CLAUDE_CODE_OAUTH_TOKEN to BOTH the main container and the
	// gateway sidecar from the per-run secret. BuildSecretEnv above only
	// covers values resolved through a K8s-secret var source — it knows
	// nothing about the dispatch-created agent-run-<id> secret — so the
	// main-container ref is set explicitly here. F20 append semantics in
	// applySecretRefs emit a secretKeyRef-only EnvVar, so no literal env
	// value is required.
	if secretRunID > 0 {
		if containerSpec.SecretEnv == nil {
			containerSpec.SecretEnv = map[string]vars.SecretRef{}
		}
		containerSpec.SecretEnv["CLAUDE_CODE_OAUTH_TOKEN"] = vars.SecretRef{Name: secretName, Key: "anthropic-token"}
	}

	// §8.5 CWD convention (F21): sidecar images never hardcode /workspace.
	// When the plan carries a `workspace` artifact, the owning exec points
	// each unset MCP-sidecar WorkingDir at its mount path; otherwise leave
	// unset (jetbridge falls back to the main container's Dir).
	wsPath := ""
	for _, n := range append(append([]string{}, step.plan.Inputs...), step.plan.Outputs...) {
		if n == "workspace" {
			wsPath = artifactPath(workdir, "workspace", "")
		}
	}

	for i := range sidecars {
		name := sidecars[i].Name
		if _, ok := mcpSidecarPorts[name]; !ok {
			continue
		}

		rows := append([]string{}, common...)
		switch name {
		case "platform":
			for _, k := range []string{"PLATFORM_MCP_ASK_TIMEOUT_POLICY", "PLATFORM_MCP_ASK_TIMEOUT_SECONDS"} {
				if v := resolvedEnv[k]; v != "" {
					rows = append(rows, k+"="+v)
				}
			}
			if secretRunID > 0 {
				setSidecarSecretRef(&containerSpec, name, "AGENT_PRINCIPAL_TOKEN",
					vars.SecretRef{Name: secretName, Key: "principal-token"})
			}
		case "gateway":
			if slice > 0 {
				rows = append(rows, "AGENT_BUDGET_SLICE_USD="+strconv.FormatFloat(slice, 'f', 2, 64))
			}
			if secretRunID > 0 {
				setSidecarSecretRef(&containerSpec, name, "AGENT_PRINCIPAL_TOKEN",
					vars.SecretRef{Name: secretName, Key: "principal-token"})
				setSidecarSecretRef(&containerSpec, name, "CLAUDE_CODE_OAUTH_TOKEN",
					vars.SecretRef{Name: secretName, Key: "anthropic-token"})
			}
			// case "dev": common+identity only
		}
		if containerSpec.SidecarEnv == nil {
			containerSpec.SidecarEnv = map[string][]string{}
		}
		containerSpec.SidecarEnv[name] = rows

		if wsPath != "" && sidecars[i].WorkingDir == "" {
			sidecars[i].WorkingDir = wsPath
		}
	}

	containerSpec.Sidecars = sidecars

	tracing.Inject(ctx, &containerSpec)

	owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)

	err = delegate.BeforeSelectWorker(logger)
	if err != nil {
		return false, err
	}

	chosenWorker, err := step.workerPool.FindOrSelectWorker(
		ctx,
		owner,
		containerSpec,
		worker.Spec{TeamID: step.metadata.TeamID},
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
	process, err := attachOrRun(
		ctx,
		container,
		runtime.ProcessSpec{
			ID:   agentProcessID,
			Path: "agent-runner",
			Dir:  workdir,
			// Guardian sets the default TTY window size to width: 80, height: 24,
			// which creates ANSI control sequences that do not work with other window sizes
			TTY: &runtime.TTYSpec{
				WindowSize: runtime.WindowSize{
					Columns: 500,
					Rows:    500,
				},
			},
		},
		sidecarProcessIO(delegate, containerSpec.Sidecars),
	)
	if err != nil {
		return false, err
	}

	result, runErr := process.Wait(ctx)

	step.registerOutputs(logger, repository, chosenWorker, outputNames, volumeMounts)

	// Synchronous server-side ingestion of the flight recorder — on EVERY
	// path where the container ran, including DeadlineExceeded and transport
	// errors, before exit handling returns. The build cannot complete (and
	// artifact-fabric retention cannot reap the events) until this is done.
	// ctx here is the timeout-scoped context; ingestFlightRecorder detaches
	// from it internally (finding F4).
	step.ingestFlightRecorder(ctx, logger, chosenWorker, volumeMounts, resolvedEnv, ticketID, verifiedRunID, time.Since(processStart))

	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			oteltrace.SpanFromContext(ctx).AddEvent("step.errored")
			delegate.Errored(logger, TimeoutLogMessage)
			return false, nil
		}

		return false, runErr
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.finished")
	delegate.Finished(logger, ExitStatus(result.ExitStatus))
	return result.ExitStatus == 0, nil
}

// ingestFlightRecorder reads flight/results.json and flight/events.ndjson
// from the registered flight output and upserts an agent_run_metrics row.
// It runs synchronously before Run returns — the build cannot complete (and
// artifact-fabric retention cannot reap the events) until ingestion is done.
// It is tolerant by design: any missing/partial/corrupt input degrades to a
// status=error row; its own failures are logged, never returned.
func (step *AgentStep) ingestFlightRecorder(
	ctx context.Context,
	logger lager.Logger,
	wkr runtime.Worker,
	volumeMounts []runtime.VolumeMount,
	resolvedEnv map[string]string,
	ticketID int,
	runID int,
	wallTime time.Duration,
) {
	if step.metricsStore == nil {
		return
	}

	// Detach from the step deadline. `ctx` is the timeout-scoped context from
	// MaybeTimeout; on the DeadlineExceeded path it is already cancelled, and
	// every StreamFile call threads ctx down to http.NewRequestWithContext,
	// so both reads below would fail instantly and a timed-out step — the
	// costliest, most measurement-critical case — would record a bare
	// status=error row with zero cost/tokens/event_counts and no ledger entry
	// (finding F4). WithoutCancel keeps the request-scoped values (tracing,
	// lager) while dropping the deadline; the fresh 30s bound keeps ingestion
	// (which blocks the build from completing) from hanging on a wedged fabric.
	ingestCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()

	rm := schema.RunMetrics{
		BuildID:         step.metadata.BuildID,
		PlanID:          string(step.planID),
		StepName:        step.plan.Name,
		WallTimeSeconds: int(wallTime.Seconds()),
	}
	// ticketID/runID are the SERVER-VERIFIED identities from run() — never
	// raw plan env. A raw claim reaching LedgerEntry.TicketID below would
	// let any pipeline drain a victim ticket's budget with spend it never
	// made (review finding, 2026-07-11).
	if ticketID > 0 {
		rm.TicketID = &ticketID
	}
	if runID > 0 {
		rm.PipelineRunID = &runID
	}
	rm.WorkflowName = resolvedEnv["AGENT_WORKFLOW_NAME"]
	if v, ok := envInt(resolvedEnv, "AGENT_WORKFLOW_VERSION"); ok {
		rm.WorkflowVersion = &v
	}
	rm.WorkflowHash = resolvedEnv["AGENT_WORKFLOW_HASH"]

	flightPath := ensureTrailingSlash(artifactPath(step.containerMetadata.WorkingDirectory, agentFlightArtifact, ""))
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
	// locator on a restart-resume, reaped pod) and the write below must go
	// through InsertIfAbsent so it can never clobber a real row (finding F24).
	flightRead := false

	if flightArtifact != nil {
		// results.json
		if rc, err := step.streamer.StreamFile(ingestCtx, flightArtifact, "results.json"); err == nil && rc != nil {
			flightRead = true
			raw, readErr := io.ReadAll(io.LimitReader(rc, 5<<20))
			rc.Close()
			var results schema.Results
			if readErr == nil && json.Unmarshal(raw, &results) == nil && results.Validate() == nil {
				status, abstained := schema.ThreeWayStatus(results.Status)
				rm.Status = status
				rm.Summary = results.Summary
				rm.Results = json.RawMessage(raw)
				if abstained {
					logger.Info("agent-abstained")
				}
			} else {
				rm.Summary = "results.json missing or malformed"
			}
		}

		// events.ndjson: counts + cost rollup + step.end detection
		if rc, err := step.streamer.StreamFile(ingestCtx, flightArtifact, "events.ndjson"); err == nil && rc != nil {
			flightRead = true
			counts := map[string]int{}
			sawStepEnd := false
			reader := schema.NewEventReader(rc)
			for {
				event, err := reader.Read()
				if err != nil {
					break // io.EOF or malformed tail — keep partial counts
				}
				counts[string(event.Type)]++
				switch event.Type {
				case schema.EventCostRecord:
					var c schema.CostRecordData
					if json.Unmarshal(event.Data, &c) == nil {
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
			rm.EventCounts = counts
			if !sawStepEnd {
				// crashed agent: a stream missing step.end is defined as error
				// (shared-contracts §5 ingestion rule)
				rm.Status = schema.RunStatusError
				if rm.Summary == "" || rm.Summary == "flight recorder output missing" {
					rm.Summary = "event stream ended without step.end"
				}
			}
		}
	}

	// Upsert reports whether THIS ingestion inserted the row (inserted=true) or
	// updated an existing one (inserted=false, i.e. a resume/retry: the whole
	// Step.Run re-executes, re-attaches, and re-ingests), and on an update also
	// returns the previous row's ledger-relevant counters. The metrics row is
	// idempotent under ON CONFLICT (build_id, plan_id), but agent_cost_ledger
	// is append-only with no dedup key (§1.4) — so the ledger append below
	// charges the DELTA against what this (build_id, plan_id) already recorded
	// (finding F3, amended for the severed-exec case): a fresh insert charges
	// rm's full cost; an update charges cost - prev.cost. Because every append
	// is itself gated on a positive delta, prev.cost is exactly what earlier
	// ingestions already appended — in particular a transient exec sever makes
	// the flight readable (F23) before the runner wrote cost.record (it only
	// does so after claude exits), so the partial ingestion inserts a ZERO-cost
	// row and appends nothing, and the resume's full ingestion (an update, so
	// a first-insert-only gate would skip it and lose the step's entire spend)
	// still charges the whole real cost. On a metrics-store error, or an
	// insert that lost a concurrent-ingestion race (inserted=false, prev=nil),
	// the delta is indeterminate and the append is skipped, preserving "every
	// dollar enters the ledger exactly once" over "at least once".
	//
	// DEGRADED ingestion (finding F24): when no flight data was actually read —
	// restart-resume with an ephemeral locator, reaped pod, missing mount — rm
	// is a zero-cost status=error shell, and pushing it through the all-columns
	// DO UPDATE would destroy a real row written by an earlier ingestion
	// (scorecards and delivery-outcomes read that row). Write it insert-only
	// instead: it lands when no row exists (genuinely crashed agent) and is a
	// no-op when one does.
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

	// prevBase is what earlier ingestions of this (build_id, plan_id) already
	// contributed to the ledger: zero on a fresh insert, the replaced row's
	// counters on an update. inserted=false with prev==nil leaves ledgerCost
	// at 0 — indeterminate, skip (see the comment above).
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
			Source:              budget.SourceAgentStep,
			Provider:            "anthropic",
			Model:               rm.Model,
			InputTokens:         max(rm.Usage.InputTokens-prevBase.Usage.InputTokens, 0),
			OutputTokens:        max(rm.Usage.OutputTokens-prevBase.Usage.OutputTokens, 0),
			CacheReadTokens:     max(rm.Usage.CacheReadInputTokens-prevBase.Usage.CacheReadInputTokens, 0),
			CacheCreationTokens: max(rm.Usage.CacheCreationInputTokens-prevBase.Usage.CacheCreationInputTokens, 0),
			Turns:               max(rm.Turns-prevBase.Turns, 0),
			CostUSD:             ledgerCost,
		}
		// Workflow attribution rides metadata->>'workflow' = "<name>@<version>"
		// — agent_cost_ledger has no workflow column and group_by=workflow
		// rollups read this key (shared-contracts §4.2 addendum: writers that
		// know their workflow MUST set it).
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
}

func envInt(env map[string]string, key string) (int, bool) {
	v, ok := env[key]
	if !ok || v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// outputNames returns the plan's declared outputs plus the implicit "flight"
// artifact, deduplicated (a user-declared `flight` output is a validation
// error upstream; dedupe defensively here).
func (step *AgentStep) outputNames() []string {
	names := make([]string, 0, len(step.plan.Outputs)+1)
	seen := map[string]bool{}
	for _, name := range append(append([]string{}, step.plan.Outputs...), agentFlightArtifact) {
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func (step *AgentStep) registerOutputs(logger lager.Logger, repository *build.Repository, worker runtime.Worker, outputNames []string, volumeMounts []runtime.VolumeMount) {
	logger.Debug("registering-outputs", lager.Data{"outputs": outputNames})

	for _, name := range outputNames {
		outputPath := artifactPath(step.containerMetadata.WorkingDirectory, name, "")

		for _, mount := range volumeMounts {
			if filepath.Clean(mount.MountPath) == filepath.Clean(outputPath) {
				// Wrap the container-mount volume as a DaemonSet-backed
				// Artifact reference before handing it to the repository —
				// without this wrap, downstream StreamOut would exec into
				// the producer pod, which breaks once the reaper deletes it.
				artifact := worker.ArtifactFromVolume(mount.Volume)
				repository.RegisterArtifact(build.ArtifactName(name), artifact, false)
			}
		}
	}
}

// mergeContainerLimits overlays the non-nil fields of override onto defaults
// (the task step's nil-field merge, applied to plan fields).
func mergeContainerLimits(override *atc.ContainerLimits, defaults atc.ContainerLimits) atc.ContainerLimits {
	merged := defaults
	if override != nil {
		if override.CPU != nil {
			merged.CPU = override.CPU
		}
		if override.Memory != nil {
			merged.Memory = override.Memory
		}
		if override.EphemeralStorage != nil {
			merged.EphemeralStorage = override.EphemeralStorage
		}
	}
	return merged
}

func setSidecarSecretRef(spec *runtime.ContainerSpec, sidecar, envName string, ref vars.SecretRef) {
	if spec.SidecarSecretEnv == nil {
		spec.SidecarSecretEnv = map[string]map[string]vars.SecretRef{}
	}
	if spec.SidecarSecretEnv[sidecar] == nil {
		spec.SidecarSecretEnv[sidecar] = map[string]vars.SecretRef{}
	}
	spec.SidecarSecretEnv[sidecar][envName] = ref
}
