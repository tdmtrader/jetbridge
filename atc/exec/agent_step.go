package exec

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/budget"
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

	// Resolve the budget slice against the ticket's remaining budget. This
	// is a resolution (min of slice and ticket remaining), NOT a
	// reservation, and it runs at the start of EVERY execution of the step
	// — a continuation build re-resolves naturally and, because any
	// park-exit partial spend was already ledgered, the re-resolved slice
	// is automatically tighter (PARK-V2 seam delta §D, decision 32).
	slice := step.plan.BudgetSliceUSD
	if step.budgetChecker != nil && slice > 0 {
		if ticketID, err := strconv.Atoi(resolvedEnv["AGENT_TICKET_ID"]); err == nil && ticketID > 0 {
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

	// Sidecar secret refs derive from the deterministic §8.2 secret name.
	// No pipeline-run id ⇒ no sidecar secret env (pure-CI agent steps get
	// sidecars without platform credentials, per §8.1).
	runID, _ := strconv.Atoi(resolvedEnv["AGENT_PIPELINE_RUN_ID"])
	secretName := "agent-run-" + strconv.Itoa(runID)

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
			if runID > 0 {
				setSidecarSecretRef(&containerSpec, name, "AGENT_PRINCIPAL_TOKEN",
					vars.SecretRef{Name: secretName, Key: "principal-token"})
			}
		case "gateway":
			if slice > 0 {
				rows = append(rows, "AGENT_BUDGET_SLICE_USD="+strconv.FormatFloat(slice, 'f', 2, 64))
			}
			if runID > 0 {
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
