package exec

import (
	"bytes"
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
	"github.com/concourse/concourse/agent/broker"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/credentials"
	"github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/compression"
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

// maxAgentInterruptionRestarts bounds how many times an interrupted agent step
// may be restarted before it fails closed.
//
// An interruption is returned as a runtime.InterruptionError so exec.RetryError
// marks the step Retriable, which is what makes agent steps behave like the
// other eight step types. But nothing on that path counts retries: the engine
// returns without finishing the build, the build tracker re-picks it up, and
// where the pod is gone -- the eviction case this exists for -- each cycle
// wipes the workspace and starts a fresh agent at full token spend, forever.
//
// The count has to outlive the web process doing the counting, so it comes
// from the durable ingestion sequence on agent_run_metrics rather than from
// memory. Three total executions is the bound: the original plus two
// restarts. At the measured $1.70 mean that caps an interrupted step near $5
// instead of leaving it unbounded.
const maxAgentInterruptionRestarts = 2

// agentFlightArtifact is the implicit output every agent step produces: the
// flight recorder directory (results.json + events.ndjson), ingested
// server-side before the step returns (docs/agentic/README.md, "Sealed
// record outputs").
const agentFlightArtifact = "flight"

// errAgentCostUnderReport marks a suspicious ingestion: the pod-written
// flight recorder claimed less cost than the web node itself observed on the
// agent's live stdout stream (review finding, 2026-07-12).
var errAgentCostUnderReport = errors.New("flight recorder reported less cost than observed on the live stdout stream")

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

// WithAgentBudgetChecker sets the checker used to append this step's spend to
// the cost ledger after flight ingestion.
func WithAgentBudgetChecker(c budget.Checker) AgentStepOption {
	return func(s *AgentStep) { s.budgetChecker = c }
}

// WithAgentPlatformTokenSecret sets the name of the K8s secret holding the
// platform Anthropic token (key "anthropic-token", plus the optional "kind"
// key). It is the ONLY model-credential path into an agent pod:
// CLAUDE_CODE_OAUTH_TOKEN and AGENT_MODEL_TOKEN_KIND are wired from it as
// secretKeyRefs, never as literal env. The web's default is the
// platform-secret syncer's own secret (credentials.PlatformSecretName).
func WithAgentPlatformTokenSecret(name string) AgentStepOption {
	return func(s *AgentStep) { s.platformTokenSecret = name }
}

// AgentTranscriptStore persists the agent tool-call transcript
// (flight/transcript.ndjson) so a run's actual behavior is inspectable.
// Satisfied by db.AgentRunTranscriptFactory.
type AgentTranscriptStore interface {
	Upsert(t db.AgentRunTranscript) error
}

// WithAgentStepTranscriptStore sets the store the agent step's own
// server-side flight ingestion upserts the runner-captured tool-call
// transcript (flight/transcript.ndjson) into (agent_run_transcripts). The
// transcript is captured by THIS step's runner — the agent step's flight
// volume is the only place it ever lives. nil disables ingestion — never
// fatal (observability is best-effort).
func WithAgentStepTranscriptStore(ts AgentTranscriptStore) AgentStepOption {
	return func(s *AgentStep) { s.transcriptStore = ts }
}

// WithAgentOutputSealer enables strict typed snapshot input and output
// execution for agent plans. The flight artifact remains outside this path.
func WithAgentOutputSealer(sealer snapshot.OutputSealer) AgentStepOption {
	return func(s *AgentStep) { s.outputSealer = sealer }
}

// WithAgentSnapshotStores supplies the durable stores used to rematerialize
// successfully sealed outputs. Downstream consumers therefore read the
// canonical snapshot content rather than the producer's mutable pod volume.
func WithAgentSnapshotStores(metadata snapshot.MetadataStore, content snapshot.ContentStore) AgentStepOption {
	return func(s *AgentStep) {
		s.snapshotMetadataStore = metadata
		s.snapshotContentStore = content
	}
}

// WithAgentInterruptionRetry makes a pod interruption retriable instead of
// terminal. It is set by the step factory only when exec.RetryError actually
// wraps the step -- that wrapper is gated on the default-off
// EnableBuildRerunWhenWorkerDisappears flag, and without it a returned
// InterruptionError is not converted to exec.Retriable: the engine finishes
// the build as errored with a raw runtime error, which is strictly worse than
// the diagnostic it replaced. The step must not assume the wrapper is there.
func WithAgentInterruptionRetry() AgentStepOption {
	return func(s *AgentStep) { s.interruptionRetry = true }
}

// WithAgentBrokerAuthorityFactory supplies the command-scoped authority
// factory that mints one already-scoped bootstrap credential and descriptor.
func WithAgentBrokerAuthorityFactory(factory AgentBrokerAuthorityFactory) AgentStepOption {
	return func(s *AgentStep) { s.agentBrokerAuthority = factory }
}

// AgentStep runs the claude CLI (via the agent-runner entrypoint) in a
// jetbridge pod with declared MCP sidecars, then ingests the flight
// recorder server-side (docs/agentic/README.md).
type AgentStep struct {
	planID                atc.PlanID
	plan                  atc.AgentPlan
	defaultLimits         atc.ContainerLimits
	defaultRequests       atc.ContainerLimits
	metadata              StepMetadata
	containerMetadata     db.ContainerMetadata
	workerPool            Pool
	streamer              Streamer
	delegateFactory       TaskDelegateFactory
	defaultTimeout        time.Duration
	agentImage            string
	imageResolver         imageresolver.Resolver
	metricsStore          metrics.Store
	budgetChecker         budget.Checker
	platformTokenSecret   string
	outputSealer          snapshot.OutputSealer
	snapshotMetadataStore snapshot.MetadataStore
	snapshotContentStore  snapshot.ContentStore
	agentBrokerAuthority  AgentBrokerAuthorityFactory
	interruptionRetry     bool

	// transcriptStore is the transcript ingestion seam — optional,
	// nil-guarded. Wired via WithAgentStepTranscriptStore.
	transcriptStore AgentTranscriptStore
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

	runtimeImage := step.agentImage
	if step.plan.RuntimeImage != "" {
		if err := atc.ValidatePinnedOCIImage(step.plan.RuntimeImage); err != nil {
			return false, fmt.Errorf("agent %q has invalid admitted runtime image: %w", step.plan.Name, err)
		}
		// Schema-v3 workflow admission injects this exact digest into the
		// immutable target config. Executing the plan must therefore use the
		// admitted dependency even when a rolling web deployment changes the
		// global default before the selected build starts. Legacy agent plans
		// leave RuntimeImage empty and continue to use the current default.
		runtimeImage = step.plan.RuntimeImage
	}
	if runtimeImage == "" {
		return false, errors.New("agent step requires the web node to be started with --agent-step-image")
	}
	if err := step.validateSnapshotDeclarations(); err != nil {
		return false, err
	}
	if candidate, governed, err := agentValidationRequirement(step.plan); err != nil {
		return false, fmt.Errorf("agent %q: authoritative validation plan is unavailable", step.plan.Name)
	} else if governed {
		if step.plan.ReviewValidation == nil {
			return false, fmt.Errorf("agent %q: authoritative validation plan is unavailable", step.plan.Name)
		}
		if err := requireValidationRequirement(ctx, fmt.Sprintf("agent %q", step.plan.Name), state.ArtifactRepository(), step.metadata, step.snapshotMetadataStore, step.snapshotContentStore, reviewRequirement(*step.plan.ReviewValidation), candidate); err != nil {
			return false, err
		}
	}
	if (len(step.plan.SnapshotInputs) > 0 || len(step.plan.SnapshotOutputs) > 0) && step.outputSealer == nil {
		return false, fmt.Errorf("agent %q has typed snapshot declarations but no output sealer is configured", step.plan.Name)
	}
	if len(step.plan.SnapshotOutputs) > 0 && (step.snapshotMetadataStore == nil || step.snapshotContentStore == nil) {
		return false, fmt.Errorf("agent %q has typed snapshot outputs but durable snapshot stores are not configured", step.plan.Name)
	}

	// Agent env is STATIC-ONLY: the renderer resolves
	// everything to literal values at render/dispatch time, and run
	// materialization interpolates ((run))/((run_id))/params into the
	// instance config before any build exists — so plan env reaches this
	// exec as literals, copied VERBATIM into the pod. It must never be
	// threaded through the build's var sources: runtime interpolation here
	// would let `env: {CLAUDE_CODE_OAUTH_TOKEN: ((vault:agent/token))}`
	// land the resolved secret as a literal pod-spec env var — readable by
	// anyone with pod read in the shared worker namespace and persisted in
	// etcd — violating the secretKeyRef-only rule (review finding,
	// 2026-07-12). A value still carrying a ((var)) reference at this point
	// was not resolved at materialization, meaning it names a runtime var
	// source (or an undeclared param): fail closed instead of interpolating
	// it or shipping the raw reference into the pod. Keys are walked in
	// sorted order so the assembled env is deterministic.
	envKeys := make([]string, 0, len(step.plan.Env))
	for k := range step.plan.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)

	planEnv := make(map[string]string, len(envKeys))
	for _, k := range envKeys {
		if k == "CONCOURSE_AGENT_RECOVERY" {
			return false, errors.New("agent recovery transport is platform-owned")
		}
		value := step.plan.Env[k]
		if refs := vars.ExtractVarRefs(value); len(refs) > 0 {
			return false, fmt.Errorf("agent env %s contains unresolved var reference ((%s)): agent env is static-only — values resolve at render/dispatch time and are never interpolated through runtime var sources", k, refs[0].String())
		}
		planEnv[k] = value
	}

	// The plan's declared slice is the step's hard cap. Admission against the
	// global daily liability happened once, durably, when the workflow run was
	// bound (agent_workflow_budget_reservations): this exec neither re-resolves
	// nor re-reserves it. 0 means uncapped — the step then runs with no
	// AGENT_BUDGET_SLICE_USD row.
	slice := step.plan.BudgetSliceUSD

	workdir := step.containerMetadata.WorkingDirectory

	// Main-container env contract (docs/agentic/README.md).
	env := step.metadata.TaskEnv()
	for _, k := range envKeys {
		env = append(env, k+"="+planEnv[k])
	}
	env = append(env, "AGENT_STEP_NAME="+step.plan.Name)
	// Exec-set identity row (never public YAML, like AGENT_STEP_NAME): the
	// runner needs the plan id to populate step.start's non-optional plan_id —
	// (build_id, plan_id) is the correlation key joining the event stream
	// back to its agent_run_metrics row, and no renderer-emitted env carries
	// it (review finding, 2026-07-12).
	env = append(env, "AGENT_PLAN_ID="+string(step.planID))
	if step.plan.Model != "" {
		env = append(env, "AGENT_MODEL="+step.plan.Model)
	}
	if step.plan.MaxTurns > 0 {
		env = append(env, "AGENT_MAX_TURNS="+strconv.Itoa(step.plan.MaxTurns))
	}
	if step.plan.Prompt != "" {
		env = append(env, "AGENT_PROMPT="+step.plan.Prompt)
	}
	env = append(env, "AGENT_FLIGHT_DIR="+artifactPath(workdir, agentFlightArtifact, ""))
	// Export every declared output's absolute in-pod path
	// (AGENT_OUTPUT_<NAME>, uppercased, dashes to underscores) so prompts
	// can target outputs deterministically. Without this the agent can only
	// guess cwd-relative paths — the first live dual-run wrote its review
	// into the repo INPUT after cd'ing there, and the output shipped empty.
	for _, name := range step.plan.Outputs {
		env = append(env, "AGENT_OUTPUT_"+strings.ToUpper(strings.ReplaceAll(name, "-", "_"))+"="+artifactPath(workdir, name, ""))
	}
	// Source-format layers (docs/agentic/README.md): resolved text travels
	// like AGENT_PROMPT; skill CONTENT travels via the "skills" input
	// artifact, so only the selected names and the mount path go here.
	if step.plan.SystemPrompt != "" {
		env = append(env, "AGENT_SYSTEM_PROMPT="+step.plan.SystemPrompt)
	}
	if step.plan.Context != "" {
		env = append(env, "AGENT_CONTEXT="+step.plan.Context)
	}
	var frozenSkills *frozenAgentSkillArtifact
	if len(step.plan.Skills) > 0 {
		env = append(env, "AGENT_SKILLS="+strings.Join(step.plan.Skills, ","))
		env = append(env, "AGENT_SKILLS_DIR="+artifactPath(workdir, "skills", ""))
	}
	if slice > 0 {
		env = append(env, "AGENT_BUDGET_SLICE_USD="+strconv.FormatFloat(slice, 'f', -1, 64))
	}

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,
		StepName: step.plan.Name,

		ImageSpec: runtime.ImageSpec{ImageURL: runtimeImage},
		Env:       env,
		Type:      step.containerMetadata.Type,
		Hermetic:  step.plan.Hermetic,

		Dir: workdir,
	}

	repository := state.ArtifactRepository()
	if len(step.plan.Skills) > 0 {
		for _, name := range append(append([]string(nil), step.plan.Inputs...), step.plan.Outputs...) {
			if name == "skills" {
				return false, fmt.Errorf("agent %q compiled skills collide with logical input or output %q", step.plan.Name, name)
			}
		}
		frozen, skillErr := newFrozenAgentSkillArtifact(step.plan.SkillFiles)
		if skillErr != nil {
			return false, fmt.Errorf("agent %q compiled skills: %w", step.plan.Name, skillErr)
		}
		frozenSkills = frozen
	}

	var missingInputs []string
	snapshotInputs := snapshotInputBindings{refs: map[string]snapshot.SnapshotRef{}}
	for _, name := range step.plan.Inputs {
		artifactName := step.inputArtifactName(name)
		if declaration, typed := step.plan.SnapshotInputs[name]; typed {
			entry, found := repository.ArtifactEntryFor(build.ArtifactName(artifactName))
			if !found {
				if !declaration.Optional {
					missingInputs = append(missingInputs, name)
				}
				continue
			}
			if entry.Artifact == nil {
				return false, fmt.Errorf("agent typed input %q has no artifact", name)
			}
			if entry.Snapshot == nil {
				return false, fmt.Errorf("agent typed input %q has no snapshot ref", name)
			}
			ref := *entry.Snapshot
			if err := ref.Validate(); err != nil {
				return false, fmt.Errorf("agent typed input %q has invalid snapshot ref: %w", name, err)
			}
			if ref.Type != declaration.Type {
				return false, fmt.Errorf("agent typed input %q snapshot type %q does not match declared type %q", name, ref.Type, declaration.Type)
			}
			destination := artifactPath(workdir, name, "")
			containerSpec.Inputs = append(containerSpec.Inputs, runtime.Input{
				Artifact: entry.Artifact, DestinationPath: destination, FromCache: entry.FromCache,
			})
			// Lookup uses the physical producer key while the logical function
			// port remains the authority, mount, and checkpoint identity.
			snapshotInputs.order = append(snapshotInputs.order, name)
			snapshotInputs.refs[name] = ref
			// An agent could read anything under this mount, so lineage records
			// the whole tree. Dynamic, agent-driven partial mounting is
			// prohibited: its path set is unknown at admission.
			snapshotInputs.recordExposure(name, ref, destination)
			continue
		}
		entry, found := repository.ArtifactEntryFor(build.ArtifactName(artifactName))
		if !found {
			missingInputs = append(missingInputs, name)
			continue
		}
		if entry.Snapshot != nil {
			return false, fmt.Errorf("agent input %q is a typed snapshot but has no input_types declaration", name)
		}
		containerSpec.Inputs = append(containerSpec.Inputs, runtime.Input{
			Artifact:        entry.Artifact,
			DestinationPath: artifactPath(workdir, name, ""),
			FromCache:       entry.FromCache,
		})
	}
	if len(missingInputs) > 0 {
		return false, MissingInputsError{missingInputs}
	}

	var (
		managedOutputBuilder *runtime.ManagedOutputBuilder
		managedAgentBroker   *runtime.ManagedAgentBroker
		agentBrokerProfiles  []broker.Profile
		err                  error
	)
	brokerRequest, brokerEnabled, err := agentBrokerAuthorityRequest(
		step.planID, step.plan, step.metadata, step.containerMetadata, snapshotInputs, workdir,
	)
	if err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}
	if brokerEnabled {
		if step.agentBrokerAuthority == nil {
			return false, fmt.Errorf("agent %q: managed agent broker authority is not configured", step.plan.Name)
		}
		managedAgentBroker, err = step.agentBrokerAuthority.BuildAgentBroker(brokerRequest)
		if err != nil {
			return false, fmt.Errorf("agent %q: build managed agent broker authority: %w", step.plan.Name, err)
		}
		agentBrokerProfiles = brokerRequest.Profiles
	}
	if step.plan.RuntimeImage != "" {
		managedOutputBuilder, err = outputBuilderAuthority(ctx, step.metadata.TeamID, step.snapshotMetadataStore, workdir, snapshotInputs, step.plan.SnapshotInputs, step.plan.SnapshotOutputs)
		if err != nil {
			return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
		}
	}
	outputTypes := make(map[string]snapshot.TypeRef, len(step.plan.SnapshotOutputs))
	for name, declaration := range step.plan.SnapshotOutputs {
		outputTypes[name] = declaration.Type
	}
	authorityEnv, err := recordAuthorityEnv(snapshotInputs, outputTypes, containerSpec.Env)
	if err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}
	containerSpec.Env = append(containerSpec.Env, authorityEnv...)

	// Declared outputs plus the implicit flight recorder output.
	outputNames := step.outputNames()
	containerSpec.Outputs = make(runtime.OutputPaths, len(outputNames))
	for _, name := range outputNames {
		containerSpec.Outputs[name] = ensureTrailingSlash(artifactPath(workdir, step.logicalOutputName(name), ""))
	}
	optionalOutputNames := make([]string, 0, len(step.plan.SnapshotOutputs))
	for _, name := range step.plan.Outputs {
		if declaration, typed := step.plan.SnapshotOutputs[name]; typed && declaration.Optional {
			optionalOutputNames = append(optionalOutputNames, name)
		}
	}
	if err := configureOptionalOutputMarkers(&containerSpec, optionalOutputNames); err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}

	limits := mergeContainerLimits(step.plan.Limits, step.defaultLimits)
	containerSpec.Limits.CPU = (*uint64)(limits.CPU)
	containerSpec.Limits.Memory = (*uint64)(limits.Memory)
	containerSpec.Limits.EphemeralStorage = (*uint64)(limits.EphemeralStorage)

	requests := mergeContainerLimits(step.plan.Requests, step.defaultRequests)
	containerSpec.Limits.CPURequest = (*uint64)(requests.CPU)
	containerSpec.Limits.MemoryRequest = (*uint64)(requests.Memory)
	containerSpec.Limits.EphemeralStorageRequest = (*uint64)(requests.EphemeralStorage)

	sidecars, err := loadSidecarConfigs(ctx, logger, repository, step.streamer, step.plan.Sidecars)
	if err != nil {
		return false, err
	}
	if err := reserveOutputBuilder(sidecars, containerSpec.Env); err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}
	if err := reserveAgentBroker(sidecars, containerSpec.Env); err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}

	err = resolveSidecarImages(ctx, logger, state, step.imageResolver, sidecars)
	if err != nil {
		return false, err
	}

	// Emit sidecar plan events so the UI can render nested sidecar steps.
	if len(sidecars) > 0 {
		delegate.EmitSidecarPlans(logger, sidecars)
	}

	// Sidecars always receive build correlation. Legacy non-hermetic agent
	// steps also retain their historical ATC/run metadata, but hermetic
	// transformation capabilities receive only local execution state. In
	// either case no sidecar receives a principal or model credential.
	common := []string{"BUILD_ID=" + strconv.Itoa(step.metadata.BuildID)}
	if !step.plan.Hermetic {
		common = append(common, "ATC_EXTERNAL_URL="+step.metadata.ExternalURL)
	}

	// The platform secret is the ONLY model-credential path into an agent
	// pod: CLAUDE_CODE_OAUTH_TOKEN is a secretKeyRef, never a literal, and
	// the credential is intentionally main-container-only — a sidecar name is
	// workflow-authored data and must never act as a credential grant.
	//
	// AGENT_MODEL_TOKEN_KIND tells the runner which CLI credential the token
	// is (anthropic_oauth | anthropic_api_key). It is OPTIONAL: the syncer
	// writes the "kind" key beside the token, but an operator-created secret
	// predating it has only "anthropic-token", and a non-optional ref would
	// make every agent pod fail to start. Absent kind means oauth.
	if step.platformTokenSecret != "" {
		containerSpec.SecretEnv = map[string]vars.SecretRef{
			"CLAUDE_CODE_OAUTH_TOKEN": {Name: step.platformTokenSecret, Key: credentials.SecretKeyAnthropicToken},
			"AGENT_MODEL_TOKEN_KIND":  {Name: step.platformTokenSecret, Key: credentials.SecretKeyModelTokenKind, Optional: true},
		}
	}

	// CWD convention (F21): sidecar images never hardcode /workspace.
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

		if containerSpec.SidecarEnv == nil {
			containerSpec.SidecarEnv = map[string][]string{}
		}
		containerSpec.SidecarEnv[name] = append([]string{}, common...)

		if wsPath != "" && sidecars[i].WorkingDir == "" {
			sidecars[i].WorkingDir = wsPath
		}
	}

	containerSpec.Sidecars = sidecars
	if err := injectManagedOutputBuilder(&containerSpec, runtimeImage, managedOutputBuilder); err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}
	if err := injectManagedAgentBroker(&containerSpec, managedAgentBroker, agentBrokerProfiles); err != nil {
		return false, fmt.Errorf("agent %q: %w", step.plan.Name, err)
	}

	tracing.Inject(ctx, &containerSpec)

	ctx, cancel, err := MaybeTimeout(ctx, step.plan.Timeout, step.defaultTimeout)
	if err != nil {
		return false, err
	}
	defer cancel()

	ctx = lagerctx.NewContext(ctx, logger)

	agentSpec := runtime.ProcessSpec{
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
	}

	var (
		chosenWorker runtime.Worker
		volumeMounts []runtime.VolumeMount
		process      runtime.Process
		result       runtime.ProcessResult
		runErr       error
		started      bool
	)

	for {
		owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)
		attemptSpec := containerSpec
		attemptSpec.Env = append([]string{}, containerSpec.Env...)
		if err := delegate.BeforeSelectWorker(logger); err != nil {
			return false, err
		}
		chosenWorker, err = step.workerPool.FindOrSelectWorker(
			ctx,
			owner,
			attemptSpec,
			worker.Spec{TeamID: step.metadata.TeamID},
		)
		if err != nil {
			return false, err
		}
		delegate.SelectedWorker(logger, chosenWorker.Name())
		if frozenSkills != nil {
			volume, _, materializeErr := chosenWorker.CreateVolumeForArtifact(ctx, step.metadata.TeamID)
			if materializeErr != nil {
				return false, fmt.Errorf("agent %q materialize compiled skills volume: %w", step.plan.Name, materializeErr)
			}
			stream, materializeErr := frozenSkills.StreamOut(ctx, ".", compression.NewGzipCompression())
			if materializeErr != nil {
				return false, fmt.Errorf("agent %q stream compiled skills: %w", step.plan.Name, materializeErr)
			}
			materializeErr = volume.StreamIn(ctx, ".", compression.NewGzipCompression(), 0, stream)
			closeErr := stream.Close()
			if materializeErr == nil {
				materializeErr = closeErr
			}
			if materializeErr != nil {
				return false, fmt.Errorf("agent %q materialize compiled skills: %w", step.plan.Name, materializeErr)
			}
			attemptSpec.Inputs = append(attemptSpec.Inputs, runtime.Input{
				Artifact: chosenWorker.ArtifactFromVolume(volume), DestinationPath: artifactPath(workdir, "skills", ""), ReadOnly: true,
			})
		}

		var container runtime.Container
		container, volumeMounts, err = chosenWorker.FindOrCreateContainer(ctx, owner, step.containerMetadata, attemptSpec, delegate)
		if err != nil {
			return false, err
		}

		if !started {
			oteltrace.SpanFromContext(ctx).AddEvent("step.starting")
			delegate.Starting(logger)
			started = true
		}
		processStart := time.Now()

		// Wrap the main-container stdout with the server-side cost observer:
		// each fresh attempt gets its own live observation window.
		pio := sidecarProcessIO(delegate, attemptSpec.Sidecars)
		costObserver := newAgentCostObserver(pio.Stdout)
		pio.Stdout = costObserver

		process, err = attachOrRun(ctx, container, agentSpec, pio)
		if err != nil {
			return false, err
		}

		result, runErr = process.Wait(ctx)

		var interruption runtime.InterruptionError
		interrupted := runErr != nil && errors.As(runErr, &interruption)

		step.registerLegacyOutputs(logger, repository, chosenWorker, outputNames, volumeMounts)

		ingestionSeq := step.ingestFlightRecorder(
			ctx,
			logger,
			chosenWorker,
			volumeMounts,
			time.Since(processStart),
			costObserver.Observed(),
		)

		if !interrupted {
			break
		}
		if !step.interruptionRetry || ingestionSeq > maxAgentInterruptionRestarts {
			if !step.interruptionRetry {
				delegate.Errored(logger, fmt.Sprintf(
					"agent execution interrupted (%s); automatic restart is not enabled on this deployment",
					interruption.InterruptionReason(),
				))
			} else {
				delegate.Errored(logger, fmt.Sprintf(
					"agent execution interrupted (%s) after %d executions; refusing a further restart",
					interruption.InterruptionReason(), ingestionSeq,
				))
			}
			return false, nil
		}
		// Retriable: RetryError wraps this, the engine leaves the build
		// started, and the tracker re-runs the plan. A surviving pod is
		// reattached by attachOrRun at no token cost; a lost one starts a
		// fresh agent, which is what the cap above bounds.
		return false, interruption
	}

	if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) {
			oteltrace.SpanFromContext(ctx).AddEvent("step.errored")
			delegate.Errored(logger, TimeoutLogMessage)
			return false, nil
		}

		return false, runErr
	}
	if result.ExitStatus == 0 && len(step.plan.SnapshotOutputs) > 0 {
		if err := step.sealTypedOutputs(ctx, repository, chosenWorker, volumeMounts, snapshotInputs); err != nil {
			return false, err
		}
	}
	oteltrace.SpanFromContext(ctx).AddEvent("step.finished")
	delegate.Finished(logger, ExitStatus(result.ExitStatus))
	return result.ExitStatus == 0, nil
}

func agentValidationRequirement(plan atc.AgentPlan) (string, bool, error) {
	question := false
	for _, output := range plan.SnapshotOutputs {
		if output.Type == snapshot.TypeRef("question/v1") {
			question = true
			break
		}
	}
	if !question {
		return "", false, nil
	}
	var candidates []string
	for _, name := range sortedSnapshotKeys(plan.SnapshotInputs) {
		if plan.SnapshotInputs[name].Type == snapshot.TypeRef("repository-change/v1") {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	if len(candidates) != 1 {
		return "", false, fmt.Errorf("multiple review candidates")
	}
	if mapped, found := plan.InputMapping[candidates[0]]; found {
		return mapped, true, nil
	}
	return candidates[0], true, nil
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
	wallTime time.Duration,
	observed observedAgentCost,
) int {
	if step.metricsStore == nil {
		return 0
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
	// The metric's execution identity is the durable workflow run and the
	// planned workflow function — both server-owned: WorkflowRunID is the
	// authenticated selected-build association carried on step.metadata (never
	// exported to step env), and FunctionID rides the immutable plan. Neither
	// is ever read from the attacker-writable flight recorder or renderer env.
	// The metric field is the schema-local WorkflowRunID; convert from the
	// snapshot type at this boundary (both are int64 underneath).
	if step.metadata.WorkflowRunID != nil {
		runID := schema.WorkflowRunID(int64(*step.metadata.WorkflowRunID))
		rm.WorkflowRunID = &runID
	}
	rm.FunctionID = step.plan.FunctionID

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
		rc, err := step.streamer.StreamFile(ingestCtx, flightArtifact, "results.json")
		if err != nil {
			// A briefly-unavailable artifact daemon degrades this ingestion to a
			// status=error / "flight recorder output missing" row. Every other
			// failure in this function is logged; without this the degradation
			// has zero diagnostics (review finding, 2026-07-12).
			logger.Error("failed-to-stream-flight-file", err, lager.Data{"file": "results.json"})
		} else if rc != nil {
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
						// Mid-stream failure (torn read, malformed tail):
						// partial counts are kept, but say so — a silent break
						// misattributes the resulting status=error to the
						// agent (review finding, 2026-07-16).
						logger.Error("flight-events-read-truncated", err)
					}
					break
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
			if n := reader.Skipped(); n > 0 {
				logger.Info("flight-events-skipped-oversized-lines", lager.Data{"count": n})
			}
			rm.EventCounts = counts
			if !sawStepEnd {
				// crashed agent: a stream missing step.end is defined as error
				// (the flight-ingestion rule), with no exceptions. The
				// PARK-V2 carve-out for a step.park-terminated stream is gone
				// along with the parked status: the runner has no park exit, and
				// a v3 human wait is a row in agent_workflow_waits on the durable
				// run, not a way for a step to end.
				rm.Status = schema.RunStatusError
				if rm.Summary == "" || rm.Summary == "flight recorder output missing" {
					rm.Summary = "event stream ended without step.end"
				}
			}
		}

		// transcript.ndjson: the raw tool-call transcript, captured by THIS
		// step's runner — the agent step is where the transcript actually
		// lives. Its own store, keyed like the metrics row on
		// (build_id, plan_id); a missing/failed transcript never affects
		// metric ingestion. Bounded to the last 512KiB server-side as
		// defense-in-depth (the runner already bounded it) — observability is
		// best-effort, never fatal.
		if step.transcriptStore != nil {
			rc, err = step.streamer.StreamFile(ingestCtx, flightArtifact, "transcript.ndjson")
			if err != nil {
				logger.Error("failed-to-stream-flight-file", err, lager.Data{"file": "transcript.ndjson"})
			} else if rc != nil {
				raw, readErr := io.ReadAll(io.LimitReader(rc, 8<<20))
				rc.Close()
				if readErr != nil {
					logger.Error("failed-to-read-flight-file", readErr, lager.Data{"file": "transcript.ndjson"})
				} else if len(raw) > 0 {
					nd, byteLen, truncated := boundTranscriptTail(raw)
					tr := db.AgentRunTranscript{
						BuildID:   step.metadata.BuildID,
						PlanID:    string(step.planID),
						StepName:  step.plan.Name,
						NDJSON:    nd,
						ByteLen:   byteLen,
						Truncated: truncated,
					}
					// Same server-owned execution identity the metrics row
					// above carries: the durable workflow run off step.metadata
					// and the function id off the immutable plan. Never read
					// from the attacker-writable flight recorder or plan env.
					if step.metadata.WorkflowRunID != nil {
						runID := *step.metadata.WorkflowRunID
						tr.WorkflowRunID = &runID
					}
					tr.FunctionID = step.plan.FunctionID
					if err := step.transcriptStore.Upsert(tr); err != nil {
						logger.Error("failed-to-ingest-transcript", err)
					}
				}
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

	// The flight recorder is SELF-REPORTED: it is written inside the agent
	// pod, where claude runs as root with --dangerously-skip-permissions and
	// AGENT_FLIGHT_DIR in its own env, so a prompt-injected agent (or a leaked
	// descendant) can truncate/rewrite events.ndjson with cost_usd 0 before
	// the pod exits. Trusting it wholesale would let the step under-report its
	// own spend: the global daily cap understates, and the binder reserves
	// every subsequent workflow run against a falsified ledger (review
	// finding, 2026-07-12). `observed` is the server-side floor: the same CLI
	// envelope the runner parses into cost.record, captured by
	// agentCostObserver as it streamed through the web node LIVE — bytes
	// already shipped cannot be rewritten at rest, and its per-field-max
	// semantics mean a forged later zero-cost envelope cannot lower it. Floor
	// every ledger-relevant counter; on the honest path the two sources carry
	// the identical float64 round-trip, so the floor is a no-op. An
	// under-report is recorded loudly as a suspicious ingestion. Limits: an
	// agent killed before the envelope prints streams nothing (partial-usage
	// capture is PARK-V2 stream-json teeing work), and a reattach may miss
	// already-streamed output — in those shapes the floor is zero and flight
	// data stands alone; admission + turn/timeout caps remain the in-step
	// levers.
	if observed.CostUSD > rm.CostUSD {
		logger.Error("flight-recorder-under-reported-cost", errAgentCostUnderReport, lager.Data{
			"build-id":          rm.BuildID,
			"plan-id":           rm.PlanID,
			"step-name":         rm.StepName,
			"flight-cost-usd":   rm.CostUSD,
			"observed-cost-usd": observed.CostUSD,
		})
		rm.CostUSD = observed.CostUSD
	}
	if rm.Model == "" {
		rm.Model = observed.Model
	}
	rm.Usage.InputTokens = max(rm.Usage.InputTokens, observed.InputTokens)
	rm.Usage.OutputTokens = max(rm.Usage.OutputTokens, observed.OutputTokens)
	rm.Usage.CacheReadInputTokens = max(rm.Usage.CacheReadInputTokens, observed.CacheReadTokens)
	rm.Usage.CacheCreationInputTokens = max(rm.Usage.CacheCreationInputTokens, observed.CacheCreationTokens)
	rm.Turns = max(rm.Turns, observed.Turns)

	// Upsert reports whether THIS ingestion inserted the row (inserted=true) or
	// updated an existing one (inserted=false, i.e. a resume/retry: the whole
	// Step.Run re-executes, re-attaches, and re-ingests), and on an update also
	// returns the previous row's ledger-relevant counters. The metrics row is
	// idempotent under ON CONFLICT (build_id, plan_id), but agent_cost_ledger
	// is append-only with no dedup key — so the ledger append below
	// charges the DELTA against what this (build_id, plan_id) already recorded
	// (finding F3, amended for the severed-exec case): a fresh insert charges
	// rm's full cost; an update charges cost - prev.cost. Because every append
	// is itself gated on a positive delta, prev.cost is exactly what earlier
	// ingestions already appended. That matters because the one-and-only first
	// insert can be consumed by a ZERO-cost write in several shapes: a
	// transient exec sever makes the flight readable (F23) before the runner
	// wrote cost.record (it only does so after claude exits); a fully DEGRADED
	// first ingestion (flightRead=false — artifact-daemon outage, mTLS hiccup)
	// lands InsertIfAbsent's zero-cost error row; or results.json streams fine
	// while the events.ndjson read fails (flightRead=true, zero-cost full
	// upsert — cost only ever comes from events). In every shape the later
	// successful re-ingestion arrives as an UPDATE — a first-insert-only gate
	// would skip it and the step's entire spend would permanently miss
	// agent_cost_ledger even though agent_run_metrics healed (review finding,
	// 2026-07-12) — and the delta prev.cost==0 → cost charges the whole real
	// amount exactly once. On a metrics-store error, or an
	// insert that lost a concurrent-ingestion race (inserted=false, prev=nil),
	// the delta is indeterminate and the append is skipped, preserving "every
	// dollar enters the ledger exactly once" over "at least once".
	//
	// DEGRADED ingestion (finding F24): when no flight data was actually read —
	// restart-resume with an ephemeral locator, reaped pod, missing mount — rm
	// is a status=error shell (zero-cost unless the stdout floor above raised
	// it), and pushing it through the all-columns DO UPDATE would destroy a
	// real row written by an earlier ingestion (scorecards and
	// delivery-outcomes read that row). Write it insert-only instead: it lands
	// when no row exists (genuinely crashed agent) and is a no-op when one
	// does.
	var inserted bool
	var prev *schema.RunMetrics
	var err error
	if flightRead {
		inserted, prev, err = step.metricsStore.UpsertReturningInserted(&rm)
	} else {
		inserted, err = step.metricsStore.InsertIfAbsent(&rm)
	}
	ingestionSeq := rm.IngestionSeq
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
		// Spend attribution is the same server-owned identity the metrics row
		// carries: the durable workflow run off step.metadata and the function
		// id off the immutable plan. Nothing here is ever read from plan env or
		// the pod-written flight recorder.
		entry := budget.LedgerEntry{
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
			FunctionID:          step.plan.FunctionID,
		}
		if step.metadata.WorkflowRunID != nil {
			workflowRunID := int64(*step.metadata.WorkflowRunID)
			entry.WorkflowRunID = &workflowRunID
		}
		if err := step.budgetChecker.Record(entry); err != nil {
			logger.Error("failed-to-record-cost-ledger", err) // fire-and-forget
		}
	}

	return ingestionSeq
}

// maxTranscriptBytes bounds a persisted transcript to its last 512KiB — the
// tail is the diagnostic part (commits/failures happen at the end).
const maxTranscriptBytes = 512 * 1024

// boundTranscriptTail keeps the last maxTranscriptBytes of a transcript,
// aligned to a line boundary, and — when it truncated — prepends the
// shared-contract head-truncation marker as the first line. Returns the
// bounded ndjson, its byte length, and whether truncation occurred.
func boundTranscriptTail(raw []byte) (string, int, bool) {
	if len(raw) <= maxTranscriptBytes {
		return string(raw), len(raw), false
	}
	tail := raw[len(raw)-maxTranscriptBytes:]
	// drop a leading partial line so the first data line is whole
	if i := bytes.IndexByte(tail, '\n'); i >= 0 && i+1 <= len(tail) {
		tail = tail[i+1:]
	}
	dropped := len(raw) - len(tail)
	marker := fmt.Sprintf(`{"type":"transcript_truncated","dropped_bytes":%d,"note":"head-truncated to last 512KiB"}`+"\n", dropped)
	out := marker + string(tail)
	return out, len(out), true
}

// outputNames returns the plan's declared outputs plus the implicit "flight"
// artifact, deduplicated (a user-declared `flight` output is a validation
// error upstream; dedupe defensively here).
func (step *AgentStep) outputNames() []string {
	names := make([]string, 0, len(step.plan.Outputs)+1)
	seen := map[string]bool{}
	for _, name := range append(append([]string{}, step.mappedOutputs()...), agentFlightArtifact) {
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

func (step *AgentStep) inputArtifactName(logical string) string {
	if mapped, found := step.plan.InputMapping[logical]; found {
		return mapped
	}
	return logical
}

func (step *AgentStep) outputArtifactName(logical string) string {
	if mapped, found := step.plan.OutputMapping[logical]; found {
		return mapped
	}
	return logical
}

func (step *AgentStep) logicalOutputName(artifact string) string {
	for logical, mapped := range step.plan.OutputMapping {
		if mapped == artifact {
			return logical
		}
	}
	return artifact
}

func (step *AgentStep) mappedOutputs() []string {
	names := make([]string, len(step.plan.Outputs))
	for index, name := range step.plan.Outputs {
		names[index] = step.outputArtifactName(name)
	}
	return names
}

func (step *AgentStep) registerLegacyOutputs(logger lager.Logger, repository *build.Repository, worker runtime.Worker, outputNames []string, volumeMounts []runtime.VolumeMount) {
	logger.Debug("registering-outputs", lager.Data{"outputs": outputNames})

	for _, name := range outputNames {
		logicalName := step.logicalOutputName(name)
		if _, typed := step.plan.SnapshotOutputs[logicalName]; typed {
			continue
		}
		outputPath := artifactPath(step.containerMetadata.WorkingDirectory, logicalName, "")

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

func (step *AgentStep) sealTypedOutputs(
	ctx context.Context,
	repository *build.Repository,
	worker runtime.Worker,
	volumeMounts []runtime.VolumeMount,
	inputs snapshotInputBindings,
) error {
	outputs, declarations, workflowDefinitionID, workflowRunID, err := step.collectTypedOutputs(ctx, worker, volumeMounts)
	if err != nil {
		return err
	}
	sources := make([]snapshot.OutputSource, len(outputs))
	for i, output := range outputs {
		sources[i] = output.source
	}
	sealed, err := step.outputSealer.Seal(ctx, snapshot.SealRequest{
		BuildID: step.metadata.BuildID, TeamID: step.metadata.TeamID, TeamName: step.metadata.TeamName,
		CreatedBy: step.metadata.SnapshotCreatedBy, PlanID: string(step.planID), Attempt: step.containerMetadata.Attempt,
		StepKind: "agent", StepName: step.plan.Name,
		WorkflowDefinitionID: workflowDefinitionID, WorkflowRunID: workflowRunID,
		InputOrder: append([]string(nil), inputs.order...), Inputs: cloneExecSnapshotRefs(inputs.refs),
		InputExposures:     cloneExecInputExposures(inputs.exposures),
		OutputDeclarations: declarations, Outputs: sources,
	})
	if err != nil {
		return err
	}
	if len(sealed) != len(outputs) {
		return fmt.Errorf("agent output sealer returned %d outputs, want %d", len(sealed), len(outputs))
	}
	entries := make(map[build.ArtifactName]build.ArtifactEntry, len(outputs))
	for _, output := range outputs {
		sealedOutput, found := sealed[output.source.ClientKey]
		if !found {
			return fmt.Errorf("agent output sealer did not return client key %q", output.source.ClientKey)
		}
		if err := sealedOutput.Validate(); err != nil {
			return fmt.Errorf("agent output sealer returned invalid output %q: %w", output.source.ClientKey, err)
		}
		if sealedOutput.Port != output.source.Port {
			return fmt.Errorf("agent output sealer returned a mismatched declaration for %q", output.source.ClientKey)
		}
		ref := sealedOutput.Snapshot
		artifact, err := materializeSealedSnapshotArtifact(
			ctx, step.metadata.TeamID, ref, step.snapshotMetadataStore, step.snapshotContentStore,
		)
		if err != nil {
			return fmt.Errorf("agent sealed output %q: %w", output.source.ClientKey, err)
		}
		entries[build.ArtifactName(output.source.ClientKey)] = build.ArtifactEntry{Artifact: artifact, Snapshot: &ref}
	}
	if err := repository.RegisterArtifacts(entries); err != nil {
		return fmt.Errorf("publish typed agent outputs: %w", err)
	}
	return nil
}

func (step *AgentStep) collectTypedOutputs(
	ctx context.Context,
	worker runtime.Worker,
	volumeMounts []runtime.VolumeMount,
) ([]collectedTypedOutput, []snapshot.Port, *int, *snapshot.WorkflowRunID, error) {
	outputs := make([]collectedTypedOutput, 0, len(step.plan.SnapshotOutputs))
	declarations := make([]snapshot.Port, 0, len(step.plan.SnapshotOutputs))
	seen := make(map[string]struct{}, len(step.plan.SnapshotOutputs))
	workflowDefinitionID, workflowRunID, err := snapshotWorkflowAssociation(
		"agent",
		step.plan.SnapshotOutputs,
		step.metadata.WorkflowDefinitionID,
		step.metadata.WorkflowRunID,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	markerArtifact, err := optionalOutputMarkerArtifact(worker, volumeMounts, hasOptionalSnapshotOutputs(step.plan.SnapshotOutputs))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("agent: %w", err)
	}
	for _, name := range step.plan.Outputs {
		artifactName := step.outputArtifactName(name)
		declaration, typed := step.plan.SnapshotOutputs[name]
		if !typed {
			continue
		}
		if _, duplicate := seen[artifactName]; duplicate {
			return nil, nil, nil, nil, fmt.Errorf("agent typed output %q is declared more than once", name)
		}
		seen[artifactName] = struct{}{}
		port := snapshot.Port{Name: name, Type: declaration.Type, Optional: declaration.Optional}
		declarations = append(declarations, port)
		outputPath := artifactPath(step.containerMetadata.WorkingDirectory, name, "")
		var artifact runtime.Artifact
		matches := 0
		for _, mount := range volumeMounts {
			if filepath.Clean(mount.MountPath) == filepath.Clean(outputPath) {
				matches++
				if matches == 1 {
					artifact = worker.ArtifactFromVolume(mount.Volume)
				}
			}
		}
		if matches > 1 {
			return nil, nil, nil, nil, fmt.Errorf("agent typed output %q has duplicate mounts", name)
		}
		if matches == 0 {
			return nil, nil, nil, nil, fmt.Errorf("agent required typed output %q has no mount", name)
		}
		if declaration.Optional {
			produced, err := optionalOutputWasProduced(ctx, markerArtifact, name)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("agent optional typed output %q: %w", name, err)
			}
			if !produced {
				continue
			}
		}
		capturedArtifact := artifact
		outputs = append(outputs, collectedTypedOutput{
			source: snapshot.OutputSource{
				ClientKey: artifactName, Port: port, Retention: declaration.Retention, WorkflowPort: declaration.WorkflowPort,
				OpenTar: func(ctx context.Context) (io.ReadCloser, error) {
					return capturedArtifact.StreamOut(ctx, ".", nil)
				},
			},
		})
	}
	return outputs, declarations, workflowDefinitionID, workflowRunID, nil
}

func (step *AgentStep) validateSnapshotDeclarations() error {
	inputs := make(map[string]struct{}, len(step.plan.Inputs))
	for _, name := range step.plan.Inputs {
		inputs[name] = struct{}{}
	}
	for _, name := range sortedSnapshotKeys(step.plan.SnapshotInputs) {
		declaration := step.plan.SnapshotInputs[name]
		if err := declaration.Validate(); err != nil {
			return fmt.Errorf("agent typed input %q: %w", name, err)
		}
		if _, found := inputs[name]; !found {
			return fmt.Errorf("agent typed input %q is absent from the declared inputs", name)
		}
	}
	outputs := make(map[string]struct{}, len(step.plan.Outputs))
	for _, name := range step.plan.Outputs {
		outputs[name] = struct{}{}
	}
	for _, name := range sortedSnapshotKeys(step.plan.SnapshotOutputs) {
		if name == agentFlightArtifact {
			return fmt.Errorf("agent flight output cannot be a typed snapshot")
		}
		if _, found := outputs[name]; !found {
			return fmt.Errorf("agent typed output %q is absent from the declared outputs", name)
		}
	}
	if len(step.plan.SnapshotInputs) > 0 || len(step.plan.SnapshotOutputs) > 0 {
		if err := validateExactArtifactCoverage("agent", "input", inputs, sortedSnapshotKeys(step.plan.SnapshotInputs)); err != nil {
			return err
		}
		if err := validateExactArtifactCoverage("agent", "output", outputs, sortedSnapshotKeys(step.plan.SnapshotOutputs)); err != nil {
			return err
		}
	}
	_, _, err := snapshotWorkflowAssociation(
		"agent",
		step.plan.SnapshotOutputs,
		step.metadata.WorkflowDefinitionID,
		step.metadata.WorkflowRunID,
	)
	return err
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
