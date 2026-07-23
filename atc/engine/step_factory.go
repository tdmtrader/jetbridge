package engine

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/agent/api/metrics"
	"github.com/concourse/concourse/agent/api/outcomes"
	"github.com/concourse/concourse/agent/api/reviews"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/budget"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/worker"
)

type coreStepFactory struct {
	pool                  worker.Pool
	streamer              worker.Streamer
	lockFactory           lock.LockFactory
	teamFactory           db.TeamFactory
	buildFactory          db.BuildFactory
	resourceCacheFactory  db.ResourceCacheFactory
	resourceConfigFactory db.ResourceConfigFactory
	defaultLimits         atc.ContainerLimits
	defaultRequests       atc.ContainerLimits
	defaultCheckTimeout   time.Duration
	defaultGetTimeout     time.Duration
	defaultPutTimeout     time.Duration
	defaultTaskTimeout    time.Duration
	imageResolver         imageresolver.Resolver
	agentStepImage        string
	agentMetricsStore     metrics.Store
	agentBudgetChecker    budget.Checker
	agentRunVerifier      exec.AgentRunVerifier
	agentPlatformToken    string
	agentTicketsStore     tickets.Store
	agentReviewsStore     reviews.Store
	agentOutcomesStore    outcomes.Store
	platformUserResolver  exec.PlatformUserResolver
	outputSealer          snapshot.OutputSealer
	snapshotMetadataStore snapshot.MetadataStore
	snapshotContentStore  snapshot.ContentStore
	snapshotInputBindings exec.WorkflowInputBindingVerifier
	snapshotCanonicalizer snapshot.Canonicalizer
	workflowWaits         workflowwait.Store
	snapshotPublisher     publisher.Executor
}

// CoreStepFactoryOption configures optional fields on coreStepFactory.
type CoreStepFactoryOption func(*coreStepFactory)

// WithCoreImageResolver sets the image resolver for sidecar digest pinning.
func WithCoreImageResolver(r imageresolver.Resolver) CoreStepFactoryOption {
	return func(f *coreStepFactory) {
		f.imageResolver = r
	}
}

// WithOutputSealer enables typed snapshot execution for both Task and Agent
// steps using one command-scoped sealer instance.
func WithOutputSealer(sealer snapshot.OutputSealer) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.outputSealer = sealer }
}

// WithSnapshotLoader enables load_snapshot using the same command-scoped
// durable stores used by sealing and one authoritative workflow verifier.
func WithSnapshotLoader(
	metadata snapshot.MetadataStore,
	content snapshot.ContentStore,
	bindings exec.WorkflowInputBindingVerifier,
) CoreStepFactoryOption {
	return func(f *coreStepFactory) {
		f.snapshotMetadataStore = metadata
		f.snapshotContentStore = content
		f.snapshotInputBindings = bindings
	}
}

// WithSnapshotCanonicalizer keeps downstream snapshot readers on the same
// bounded, deployment-owned scratch storage as sealing and durable uploads.
func WithSnapshotCanonicalizer(canonicalizer snapshot.Canonicalizer) CoreStepFactoryOption {
	return func(f *coreStepFactory) {
		f.snapshotCanonicalizer = canonicalizer
	}
}

func WithWorkflowWaitStore(store workflowwait.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.workflowWaits = store }
}

// WithSnapshotPublisher enables explicit publish_snapshot execution. Leaving
// it unset keeps every publisher node fail-closed; no external write can be
// inferred from an ordinary output or successful build.
func WithSnapshotPublisher(executor publisher.Executor) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.snapshotPublisher = executor }
}

// WithAgentStepImage sets the main-container image for agent: steps
// (web flag --agent-step-image).
func WithAgentStepImage(image string) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentStepImage = image }
}

// WithAgentTicketsStore wires the single-writer ticket store so the
// harvest step can transition tickets after the runner exits.
func WithAgentTicketsStore(s tickets.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentTicketsStore = s }
}

// WithAgentMetricsStore sets the run-metrics store for server-side
// flight-recorder ingestion.
func WithAgentMetricsStore(s metrics.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentMetricsStore = s }
}

// WithAgentReviewsStore sets the store the harvest step upserts judged
// review.json evidence into (agent_reviews) during server-side flight
// ingestion.
func WithAgentReviewsStore(s reviews.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentReviewsStore = s }
}

// WithAgentOutcomesStore sets the delivery-outcomes store the harvest
// step seeds the agent_outcomes row into (authoritative pushed/base
// shas) after a successful push.
func WithAgentOutcomesStore(s outcomes.Store) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentOutcomesStore = s }
}

// WithAgentPlatformUserResolver sets the §1.13 platform-user lookup
// (UserBySub) so harvest_judge ledger rows carry platform attribution.
func WithAgentPlatformUserResolver(r exec.PlatformUserResolver) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.platformUserResolver = r }
}

// WithAgentBudgetChecker sets the budget library used for step-slice
// resolution and fire-and-forget ledger records.
func WithAgentBudgetChecker(c budget.Checker) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentBudgetChecker = c }
}

// WithAgentRunVerifier sets the seam that proves a plan-env-claimed
// pipeline-run id belongs to this build's pipeline before agent-step
// sidecar secret refs (§8.2) are injected.
func WithAgentRunVerifier(v exec.AgentRunVerifier) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentRunVerifier = v }
}

// WithAgentPlatformTokenSecret sets the K8s secret name providing the
// pure-CI Anthropic token (web flag --agent-platform-token-secret).
func WithAgentPlatformTokenSecret(name string) CoreStepFactoryOption {
	return func(f *coreStepFactory) { f.agentPlatformToken = name }
}

func NewCoreStepFactory(
	pool worker.Pool,
	streamer worker.Streamer,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	resourceCacheFactory db.ResourceCacheFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	defaultCheckTimeout time.Duration,
	defaultGetTimeout time.Duration,
	defaultPutTimeout time.Duration,
	defaultTaskTimeout time.Duration,
	opts ...CoreStepFactoryOption,
) CoreStepFactory {
	f := &coreStepFactory{
		pool:                  pool,
		streamer:              streamer,
		lockFactory:           lockFactory,
		teamFactory:           teamFactory,
		buildFactory:          buildFactory,
		resourceCacheFactory:  resourceCacheFactory,
		resourceConfigFactory: resourceConfigFactory,
		defaultLimits:         defaultLimits,
		defaultRequests:       defaultRequests,
		defaultCheckTimeout:   defaultCheckTimeout,
		defaultGetTimeout:     defaultGetTimeout,
		defaultPutTimeout:     defaultPutTimeout,
		defaultTaskTimeout:    defaultTaskTimeout,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func (factory *coreStepFactory) GetStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = resource.ResourcesDir("get")

	getStep := exec.NewGetStep(
		plan.ID,
		*plan.Get,
		stepMetadata,
		containerMetadata,
		factory.lockFactory,
		factory.resourceCacheFactory,
		delegateFactory,
		factory.pool,
		factory.defaultGetTimeout,
	)

	getStep = exec.LogError(getStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		getStep = exec.RetryError(getStep, delegateFactory)
	}
	return getStep
}

func (factory *coreStepFactory) PutStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = resource.ResourcesDir("put")

	putStep := exec.NewPutStep(
		plan.ID,
		*plan.Put,
		stepMetadata,
		containerMetadata,
		factory.pool,
		delegateFactory,
		factory.defaultPutTimeout,
	)

	putStep = exec.LogError(putStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		putStep = exec.RetryError(putStep, delegateFactory)
	}
	return putStep
}

func (factory *coreStepFactory) CheckStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = resource.ResourcesDir("check")

	var checkOpts []exec.CheckStepOption
	if factory.imageResolver != nil {
		checkOpts = append(checkOpts, exec.WithCheckResolver(factory.imageResolver))
	}

	checkStep := exec.NewCheckStep(
		plan.ID,
		*plan.Check,
		stepMetadata,
		factory.resourceConfigFactory,
		containerMetadata,
		factory.pool,
		delegateFactory,
		factory.defaultCheckTimeout,
		checkOpts...,
	)

	checkStep = exec.LogError(checkStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		checkStep = exec.RetryError(checkStep, delegateFactory)
	}
	return checkStep
}

func (factory *coreStepFactory) RunStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = "/tmp/build/run"

	runStep := exec.NewRunStep(
		plan.ID,
		*plan.Run,
		delegateFactory,
	)

	runStep = exec.LogError(runStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		runStep = exec.RetryError(runStep, delegateFactory)
	}
	return runStep
}

func (factory *coreStepFactory) AgentStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	sum := sha256.Sum256([]byte(plan.Agent.Name))
	containerMetadata.WorkingDirectory = filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))

	var agentOpts []exec.AgentStepOption
	if factory.imageResolver != nil {
		agentOpts = append(agentOpts, exec.WithAgentImageResolver(factory.imageResolver))
	}
	if factory.agentMetricsStore != nil {
		agentOpts = append(agentOpts, exec.WithAgentMetricsStore(factory.agentMetricsStore))
	}
	if factory.agentBudgetChecker != nil {
		agentOpts = append(agentOpts, exec.WithAgentBudgetChecker(factory.agentBudgetChecker))
	}
	if factory.agentRunVerifier != nil {
		agentOpts = append(agentOpts, exec.WithAgentRunVerifier(factory.agentRunVerifier))
	}
	if factory.agentPlatformToken != "" {
		agentOpts = append(agentOpts, exec.WithAgentPlatformTokenSecret(factory.agentPlatformToken))
	}
	if factory.outputSealer != nil {
		agentOpts = append(agentOpts, exec.WithAgentOutputSealer(factory.outputSealer))
	}
	if factory.snapshotMetadataStore != nil && factory.snapshotContentStore != nil {
		agentOpts = append(agentOpts, exec.WithAgentSnapshotStores(factory.snapshotMetadataStore, factory.snapshotContentStore))
	}

	agentStep := exec.NewAgentStep(
		plan.ID,
		*plan.Agent,
		factory.defaultLimits,
		factory.defaultRequests,
		stepMetadata,
		containerMetadata,
		factory.pool,
		factory.streamer,
		delegateFactory,
		factory.defaultTaskTimeout,
		factory.agentStepImage,
		agentOpts...,
	)

	agentStep = exec.LogError(agentStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		agentStep = exec.RetryError(agentStep, delegateFactory)
	}
	return agentStep
}

func (factory *coreStepFactory) HarvestStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	sum := sha256.Sum256([]byte(plan.Harvest.Name))
	containerMetadata.WorkingDirectory = filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))

	// The streamer rides unconditionally: ingestion is nil-guarded on the
	// metrics store, not the streamer.
	harvestOpts := []exec.HarvestStepOption{exec.WithHarvestStreamer(factory.streamer)}
	if factory.agentTicketsStore != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestTicketsStore(factory.agentTicketsStore))
	}
	if factory.agentRunVerifier != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestRunVerifier(factory.agentRunVerifier))
	}
	if factory.agentPlatformToken != "" {
		harvestOpts = append(harvestOpts, exec.WithHarvestPlatformTokenSecret(factory.agentPlatformToken))
	}
	if factory.agentMetricsStore != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestMetricsStore(factory.agentMetricsStore))
	}
	if factory.agentReviewsStore != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestReviewsStore(factory.agentReviewsStore))
	}
	if factory.agentOutcomesStore != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestOutcomesStore(factory.agentOutcomesStore))
	}
	if factory.agentBudgetChecker != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestBudgetRecorder(factory.agentBudgetChecker))
	}
	if factory.platformUserResolver != nil {
		harvestOpts = append(harvestOpts, exec.WithHarvestPlatformUserResolver(factory.platformUserResolver))
	}

	harvestStep := exec.NewHarvestStep(
		plan.ID,
		*plan.Harvest,
		stepMetadata,
		containerMetadata,
		factory.pool,
		delegateFactory,
		factory.defaultTaskTimeout,
		factory.agentStepImage,
		harvestOpts...,
	)

	harvestStep = exec.LogError(harvestStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		harvestStep = exec.RetryError(harvestStep, delegateFactory)
	}
	return harvestStep
}

func (factory *coreStepFactory) TaskStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	sum := sha256.Sum256([]byte(plan.Task.Name))
	containerMetadata.WorkingDirectory = filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))

	var taskOpts []exec.TaskStepOption
	if factory.imageResolver != nil {
		taskOpts = append(taskOpts, exec.WithImageResolver(factory.imageResolver))
	}
	if factory.outputSealer != nil {
		taskOpts = append(taskOpts, exec.WithTaskOutputSealer(factory.outputSealer))
	}
	if factory.snapshotMetadataStore != nil && factory.snapshotContentStore != nil {
		taskOpts = append(taskOpts, exec.WithTaskSnapshotStores(factory.snapshotMetadataStore, factory.snapshotContentStore))
	}

	taskStep := exec.NewTaskStep(
		plan.ID,
		*plan.Task,
		factory.defaultLimits,
		factory.defaultRequests,
		stepMetadata,
		containerMetadata,
		factory.pool,
		factory.streamer,
		delegateFactory,
		factory.defaultTaskTimeout,
		taskOpts...,
	)

	taskStep = exec.LogError(taskStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		taskStep = exec.RetryError(taskStep, delegateFactory)
	}
	return taskStep
}

func (factory *coreStepFactory) SetPipelineStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	spStep := exec.NewSetPipelineStep(
		plan.ID,
		*plan.SetPipeline,
		stepMetadata,
		delegateFactory,
		factory.teamFactory,
		factory.buildFactory,
		factory.streamer,
	)

	spStep = exec.LogError(spStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		spStep = exec.RetryError(spStep, delegateFactory)
	}
	return spStep
}

func (factory *coreStepFactory) LoadVarStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	loadVarStep := exec.NewLoadVarStep(
		plan.ID,
		*plan.LoadVar,
		stepMetadata,
		delegateFactory,
		factory.streamer,
	)

	loadVarStep = exec.LogError(loadVarStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		loadVarStep = exec.RetryError(loadVarStep, delegateFactory)
	}
	return loadVarStep
}

func (factory *coreStepFactory) LoadSnapshotStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	loadSnapshotStep := exec.NewLoadSnapshotStep(
		plan.ID,
		*plan.LoadSnapshot,
		stepMetadata,
		delegateFactory,
		factory.snapshotMetadataStore,
		factory.snapshotContentStore,
		factory.snapshotInputBindings,
	)

	loadSnapshotStep = exec.LogError(loadSnapshotStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		loadSnapshotStep = exec.RetryError(loadSnapshotStep, delegateFactory)
	}
	return loadSnapshotStep
}

func (factory *coreStepFactory) AwaitSnapshotStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	waitStep := exec.NewAwaitSnapshotStep(
		plan.ID,
		plan.Attempts,
		*plan.AwaitSnapshot,
		stepMetadata,
		delegateFactory,
		factory.workflowWaits,
		factory.outputSealer,
		factory.snapshotMetadataStore,
		factory.snapshotContentStore,
		500*time.Millisecond,
	)
	waitStep = exec.LogError(waitStep, delegateFactory)
	return waitStep
}

func (factory *coreStepFactory) PublishSnapshotStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	var approvalVerifier publisher.MergeApprovalVerifier
	if factory.workflowWaits != nil && factory.snapshotMetadataStore != nil && factory.snapshotContentStore != nil {
		approvalVerifier, _ = publisher.NewDurableApprovalVerifier(
			factory.workflowWaits,
			factory.snapshotMetadataStore,
			factory.snapshotContentStore,
			publisher.WithApprovalCanonicalizer(factory.snapshotCanonicalizer),
		)
	}
	publishStep := exec.NewPublishSnapshotStep(
		plan.ID,
		*plan.PublishSnapshot,
		stepMetadata,
		delegateFactory,
		factory.snapshotMetadataStore,
		factory.snapshotPublisher,
		approvalVerifier,
	)
	return exec.LogError(publishStep, delegateFactory)
}

func (factory *coreStepFactory) ArtifactInputStep(
	plan atc.Plan,
	build db.Build,
) exec.Step {
	return exec.NewArtifactInputStep(plan, build, factory.pool)
}

func (factory *coreStepFactory) ArtifactOutputStep(
	plan atc.Plan,
	build db.Build,
) exec.Step {
	return exec.NewArtifactOutputStep(plan, build, factory.pool)
}
