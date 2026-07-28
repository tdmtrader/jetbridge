package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/exec/build"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	"go.opentelemetry.io/otel/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const taskProcessID = "task"

// MissingInputsError is returned when any of the task's required inputs are
// missing.
type MissingInputsError struct {
	Inputs []string
}

// Error prints a human-friendly message listing the inputs that were missing.
func (err MissingInputsError) Error() string {
	return fmt.Sprintf("missing inputs: %s", strings.Join(err.Inputs, ", "))
}

type MissingTaskImageSourceError struct {
	SourceName string
}

func (err MissingTaskImageSourceError) Error() string {
	return fmt.Sprintf(`missing image artifact source: %s

make sure there's a corresponding 'get' step, or a task that produces it as an output`, err.SourceName)
}

type TaskImageSourceParametersError struct {
	Err error
}

func (err TaskImageSourceParametersError) Error() string {
	return fmt.Sprintf("failed to evaluate image resource parameters: %s", err.Err)
}

//counterfeiter:generate . TaskDelegateFactory
type TaskDelegateFactory interface {
	TaskDelegate(state RunState) TaskDelegate
}

//counterfeiter:generate . TaskDelegate
type TaskDelegate interface {
	StartSpan(context.Context, string, tracing.Attrs) (context.Context, trace.Span)

	FetchImage(context.Context, atc.ImageResource, atc.ResourceTypes, bool, atc.Tags, bool) (runtime.ImageSpec, error)

	Stdout() io.Writer
	Stderr() io.Writer

	SetTaskConfig(config atc.TaskConfig)
	EmitSidecarPlans(lager.Logger, []atc.SidecarConfig)
	SidecarWriter(sidecarName string) io.Writer

	Initializing(lager.Logger)
	Starting(lager.Logger)
	Finished(lager.Logger, ExitStatus)
	Errored(lager.Logger, string)

	BeforeSelectWorker(lager.Logger) error
	WaitingForWorker(lager.Logger)
	SelectedWorker(lager.Logger, string)
	BuildStartTime() time.Time
}

// TaskStepOption configures optional fields on a TaskStep.
type TaskStepOption func(*TaskStep)

// WithImageResolver sets an image resolver for sidecar digest pinning.
func WithImageResolver(r imageresolver.Resolver) TaskStepOption {
	return func(s *TaskStep) {
		s.imageResolver = r
	}
}

// WithTaskOutputSealer enables strict typed snapshot input and output
// execution for task plans. Typed plans fail closed when this option is not
// provided; legacy plans do not consult it.
func WithTaskOutputSealer(sealer snapshot.OutputSealer) TaskStepOption {
	return func(s *TaskStep) { s.outputSealer = sealer }
}

// WithTaskSnapshotStores supplies the durable stores used to rematerialize
// successfully sealed outputs. The registered downstream artifact is always
// backed by these stores, never by the producer's still-mutable output mount.
func WithTaskSnapshotStores(metadata snapshot.MetadataStore, content snapshot.ContentStore) TaskStepOption {
	return func(s *TaskStep) {
		s.snapshotMetadataStore = metadata
		s.snapshotContentStore = content
	}
}

// TaskStep executes a TaskConfig, whose inputs will be fetched from the
// artifact.Repository and outputs will be added to the artifact.Repository.
type TaskStep struct {
	planID                atc.PlanID
	plan                  atc.TaskPlan
	defaultLimits         atc.ContainerLimits
	defaultRequests       atc.ContainerLimits
	metadata              StepMetadata
	containerMetadata     db.ContainerMetadata
	workerPool            Pool
	streamer              Streamer
	delegateFactory       TaskDelegateFactory
	defaultTaskTimeout    time.Duration
	imageResolver         imageresolver.Resolver
	outputSealer          snapshot.OutputSealer
	snapshotMetadataStore snapshot.MetadataStore
	snapshotContentStore  snapshot.ContentStore
}

func NewTaskStep(
	planID atc.PlanID,
	plan atc.TaskPlan,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	metadata StepMetadata,
	containerMetadata db.ContainerMetadata,
	workerPool Pool,
	streamer Streamer,
	delegateFactory TaskDelegateFactory,
	defaultTaskTimeout time.Duration,
	opts ...TaskStepOption,
) Step {
	s := &TaskStep{
		planID:             planID,
		plan:               plan,
		defaultLimits:      defaultLimits,
		defaultRequests:    defaultRequests,
		metadata:           metadata,
		containerMetadata:  containerMetadata,
		workerPool:         workerPool,
		streamer:           streamer,
		delegateFactory:    delegateFactory,
		defaultTaskTimeout: defaultTaskTimeout,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Run will first select the worker based on the TaskConfig's platform and the
// TaskStep's tags, and prioritize it by availability of volumes for the TaskConfig's
// inputs. Inputs that did not have volumes available on the worker will be streamed
// in to the container.
//
// If any inputs are not available in the artifact.Repository, MissingInputsError
// is returned.
//
// Once all the inputs are satisfied, the task's script will be executed. If
// the task is canceled via the context, the script will be interrupted.
//
// If the script exits successfully, the outputs specified in the TaskConfig
// are registered with the artifact.Repository. If no outputs are specified, the
// task's entire working directory is registered as an StreamableArtifactSource under the
// name of the task.
func (step *TaskStep) Run(ctx context.Context, state RunState) (bool, error) {
	start := time.Now()
	delegate := step.delegateFactory.TaskDelegate(state)
	ctx, span := delegate.StartSpan(ctx, "task", tracing.Attrs{
		"name": step.plan.Name,
	})

	ok, err := step.run(ctx, state, delegate)
	tracing.End(span, err)
	metric.RecordStepDuration(ctx, "task", step.plan.Name, time.Since(start))

	return ok, err
}

func (step *TaskStep) run(ctx context.Context, state RunState, delegate TaskDelegate) (bool, error) {
	logger := lagerctx.FromContext(ctx)
	logger = tracing.LoggerWithSpan(ctx, logger)
	logger = logger.Session("task-step", lager.Data{
		"step-name": step.plan.Name,
		"job-id":    step.metadata.JobID,
	})

	var taskConfigSource TaskConfigSource
	var taskVars []vars.Variables

	if step.plan.ConfigPath != "" {
		// external task - construct a source which reads it from file, and apply base resource type defaults.
		taskConfigSource = FileConfigSource{ConfigPath: step.plan.ConfigPath, Streamer: step.streamer}

		// for interpolation - use 'vars' from the pipeline, and then fill remaining with cred variables.
		// this 2-phase strategy allows to interpolate 'vars' by cred variables.
		if len(step.plan.Vars) > 0 {
			taskConfigSource = InterpolateTemplateConfigSource{
				ConfigSource:  taskConfigSource,
				Vars:          []vars.Variables{vars.StaticVariables(step.plan.Vars)},
				ExpectAllKeys: false,
			}
		}
		taskVars = []vars.Variables{state}
	} else {
		// embedded task - first we take it
		taskConfigSource = StaticConfigSource{Config: step.plan.Config}

		// for interpolation - use just cred variables
		taskVars = []vars.Variables{state}
	}

	// apply resource type defaults
	taskConfigSource = BaseResourceTypeDefaultsApplySource{
		ConfigSource:  taskConfigSource,
		ResourceTypes: step.plan.ResourceTypes,
	}

	// override limits and requests
	taskConfigSource = &OverrideContainerLimitsSource{ConfigSource: taskConfigSource, Limits: step.plan.Limits, Requests: step.plan.Requests}

	// override params
	taskConfigSource = &OverrideParamsConfigSource{ConfigSource: taskConfigSource, Params: step.plan.Params}

	// interpolate template vars
	taskConfigSource = InterpolateTemplateConfigSource{
		ConfigSource:  taskConfigSource,
		Vars:          taskVars,
		ExpectAllKeys: true,
	}

	// validate
	taskConfigSource = ValidatingConfigSource{ConfigSource: taskConfigSource}

	repository := state.ArtifactRepository()
	if step.plan.ConfigPath != "" {
		parts := strings.SplitN(step.plan.ConfigPath, "/", 2)
		if len(parts) == 2 {
			if entry, found := repository.ArtifactEntryFor(build.ArtifactName(parts[0])); found && entry.Snapshot != nil {
				return false, fmt.Errorf("task config source %q is a typed snapshot and cannot be consumed without a typed task input", parts[0])
			}
		}
	}

	config, err := taskConfigSource.FetchConfig(ctx, logger, repository)

	for _, warning := range taskConfigSource.Warnings() {
		fmt.Fprintln(delegate.Stderr(), "[WARNING]", warning)
	}

	if err != nil {
		return false, err
	}
	if err := step.validateSnapshotDeclarations(config); err != nil {
		return false, err
	}
	if err := step.validateDevValidationTask(config); err != nil {
		return false, err
	}
	if (len(step.plan.SnapshotInputs) > 0 || len(step.plan.SnapshotOutputs) > 0) && step.outputSealer == nil {
		return false, fmt.Errorf("task %q has typed snapshot declarations but no output sealer is configured", step.plan.Name)
	}
	if len(step.plan.SnapshotOutputs) > 0 && (step.snapshotMetadataStore == nil || step.snapshotContentStore == nil) {
		return false, fmt.Errorf("task %q has typed snapshot outputs but durable snapshot stores are not configured", step.plan.Name)
	}

	if config.Limits == nil {
		config.Limits = &atc.ContainerLimits{}
	}
	if config.Limits.CPU == nil {
		config.Limits.CPU = step.defaultLimits.CPU
	}
	if config.Limits.Memory == nil {
		config.Limits.Memory = step.defaultLimits.Memory
	}

	if step.defaultRequests.CPU != nil || step.defaultRequests.Memory != nil {
		if config.Requests == nil {
			config.Requests = &atc.ContainerLimits{}
		}
		if config.Requests.CPU == nil {
			config.Requests.CPU = step.defaultRequests.CPU
		}
		if config.Requests.Memory == nil {
			config.Requests.Memory = step.defaultRequests.Memory
		}
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.initializing")
	delegate.Initializing(logger)

	imageSpec, err := step.imageSpec(ctx, logger, state, delegate, config)
	if err != nil {
		return false, err
	}

	if config.Run.Path == "" {
		path, args, err := step.getOciEntrypoint(ctx, imageSpec)
		if err != nil {
			return false, err
		}

		config.Run.Path = path
		// Append any args defined in the Task config to those from the
		// container image
		config.Run.Args = append(args, config.Run.Args...)
	}

	if config.Run.Path == "" {
		return false, errors.New("No executable defined for task. Specify an executable to run in either the task config (run.path) or as an ENTRYPOINT/CMD in the container image.")
	}

	delegate.SetTaskConfig(config)

	containerSpec, snapshotInputs, err := step.containerSpec(logger, state, imageSpec, config, step.containerMetadata)
	if err != nil {
		return false, err
	}
	if err := step.bindDevValidationCandidate(&config, snapshotInputs); err != nil {
		return false, err
	}

	containerSpec.Sidecars, err = loadSidecarConfigs(ctx, logger, state.ArtifactRepository(), step.streamer, step.plan.Sidecars)
	if err != nil {
		return false, err
	}
	err = resolveSidecarImages(ctx, logger, state, step.imageResolver, containerSpec.Sidecars)
	if err != nil {
		return false, err
	}

	// Emit sidecar plan events so the UI can render nested sidecar steps.
	if len(containerSpec.Sidecars) > 0 {
		delegate.EmitSidecarPlans(logger, containerSpec.Sidecars)
	}

	tracing.Inject(ctx, &containerSpec)

	owner := db.NewBuildStepContainerOwner(step.metadata.BuildID, step.planID, step.metadata.TeamID)

	err = delegate.BeforeSelectWorker(logger)
	if err != nil {
		return false, err
	}

	worker, err := step.workerPool.FindOrSelectWorker(
		ctx,
		owner,
		containerSpec,
		step.workerSpec(config),
	)
	if err != nil {
		return false, err
	}
	if err := step.addDevValidationAuthorityInput(&containerSpec); err != nil {
		return false, err
	}

	ctx, cancel, err := MaybeTimeout(ctx, step.plan.Timeout, step.defaultTaskTimeout)
	if err != nil {
		return false, err
	}
	defer cancel()

	ctx = lagerctx.NewContext(ctx, logger)

	delegate.SelectedWorker(logger, worker.Name())

	container, volumeMounts, err := worker.FindOrCreateContainer(ctx, owner, step.containerMetadata, containerSpec, delegate)
	if err != nil {
		return false, err
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.starting")
	delegate.Starting(logger)
	process, err := attachOrRun(
		ctx,
		container,
		runtime.ProcessSpec{
			ID:   taskProcessID,
			Path: config.Run.Path,
			Args: config.Run.Args,
			Dir:  resolvePath(step.containerMetadata.WorkingDirectory, config.Run.Dir),
			User: config.Run.User,
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

	step.registerLegacyOutputs(logger, repository, worker, config, volumeMounts, step.containerMetadata)

	// Do not initialize caches for one-off builds
	if step.metadata.JobID != 0 {
		if err := step.registerCaches(ctx, repository, config, volumeMounts, step.containerMetadata); err != nil {
			return false, err
		}
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
		if err := step.sealTypedOutputs(ctx, repository, worker, config, volumeMounts, snapshotInputs); err != nil {
			return false, err
		}
	}

	oteltrace.SpanFromContext(ctx).AddEvent("step.finished")
	delegate.Finished(logger, ExitStatus(result.ExitStatus))
	return result.ExitStatus == 0, nil
}

func attachOrRun(ctx context.Context, container runtime.Container, spec runtime.ProcessSpec, io runtime.ProcessIO) (runtime.Process, error) {
	process, err := container.Attach(ctx, spec.ID, io)
	if err == nil {
		return process, nil
	}
	return container.Run(ctx, spec, io)
}

func (step *TaskStep) imageSpec(ctx context.Context, logger lager.Logger, state RunState, delegate TaskDelegate, config atc.TaskConfig) (runtime.ImageSpec, error) {
	imageSpec := runtime.ImageSpec{
		Privileged: bool(step.plan.Privileged),
	}

	// Determine the source of the container image
	// a reference to an artifact (get step, task output) ?
	if step.plan.ImageArtifactName != "" {
		entry, found := state.ArtifactRepository().ArtifactEntryFor(build.ArtifactName(step.plan.ImageArtifactName))
		if !found {
			return runtime.ImageSpec{}, MissingTaskImageSourceError{step.plan.ImageArtifactName}
		}
		if entry.Snapshot != nil {
			return runtime.ImageSpec{}, fmt.Errorf("task image source %q is a typed snapshot and cannot be consumed as an untyped image artifact", step.plan.ImageArtifactName)
		}
		artifact := entry.Artifact
		if imageRef, ok := state.ArtifactRepository().ImageRefFor(build.ArtifactName(step.plan.ImageArtifactName)); ok {
			// Prefer the image ref URL when available — JetBridge's
			// container runtime pulls images natively via the URL.
			// The artifact volume (if present) is still available as
			// a build input for steps that need the OCI content.
			imageSpec.ImageURL = imageRef
		} else if artifact != nil {
			imageSpec.ImageArtifact = artifact
		}

		//an image_resource
	} else if config.ImageResource != nil {
		imageSpec, err := delegate.FetchImage(
			ctx,
			*config.ImageResource,
			step.plan.ResourceTypes,
			step.plan.Privileged,
			step.plan.Tags,
			step.plan.CheckSkipInterval,
		)
		return imageSpec, err

		// a rootfs_uri
	} else if config.RootfsURI != "" {
		imageSpec.ImageURL = config.RootfsURI
	}

	return imageSpec, nil
}

type snapshotInputBindings struct {
	order []string
	refs  map[string]snapshot.SnapshotRef
	// exposures is the mount-time exposure lineage for the bound input ports:
	// how much of each tree the step was actually shown, and where it was
	// mounted. The runtime materializes whole artifacts, so every entry is
	// full-tree — which is also the required default for agentic steps. It is
	// occurrence data and is persisted outside the sealed bytes.
	exposures map[string]snapshot.InputExposure
}

// recordExposure captures one input's materialization at the moment the mount
// is declared, so the lineage cannot drift from the mount it describes.
func (bindings *snapshotInputBindings) recordExposure(port string, ref snapshot.SnapshotRef, mountPath string) {
	if bindings.exposures == nil {
		bindings.exposures = map[string]snapshot.InputExposure{}
	}
	bindings.exposures[port] = snapshot.FullTreeExposure(mountPath, ref.Digest)
}

func (step *TaskStep) containerInputs(logger lager.Logger, repository *build.Repository, config atc.TaskConfig, metadata db.ContainerMetadata) ([]runtime.Input, snapshotInputBindings, error) {
	var inputs []runtime.Input
	bindings := snapshotInputBindings{refs: map[string]snapshot.SnapshotRef{}}

	var missingRequiredInputs []string

	for _, input := range config.Inputs {
		inputName := input.Name
		if sourceName, ok := step.plan.InputMapping[inputName]; ok {
			inputName = sourceName
		}

		if declaration, typed := step.plan.SnapshotInputs[inputName]; typed {
			entry, found := repository.ArtifactEntryFor(build.ArtifactName(inputName))
			if !found {
				if !declaration.Optional {
					missingRequiredInputs = append(missingRequiredInputs, inputName)
				}
				continue
			}
			if entry.Artifact == nil {
				return nil, snapshotInputBindings{}, fmt.Errorf("task typed input %q has no artifact", inputName)
			}
			if entry.Snapshot == nil {
				return nil, snapshotInputBindings{}, fmt.Errorf("task typed input %q has no snapshot ref", inputName)
			}
			ref := *entry.Snapshot
			if err := ref.Validate(); err != nil {
				return nil, snapshotInputBindings{}, fmt.Errorf("task typed input %q has invalid snapshot ref: %w", inputName, err)
			}
			if ref.Type != declaration.Type {
				return nil, snapshotInputBindings{}, fmt.Errorf("task typed input %q snapshot type %q does not match declared type %q", inputName, ref.Type, declaration.Type)
			}
			destination := artifactPath(metadata.WorkingDirectory, input.Name, input.Path)
			inputs = append(inputs, runtime.Input{
				Artifact: entry.Artifact, DestinationPath: destination,
				FromCache: entry.FromCache, ReadOnly: step.readOnlyInput(inputName),
			})
			bindings.order = append(bindings.order, inputName)
			bindings.refs[inputName] = ref
			bindings.recordExposure(inputName, ref, destination)
			continue
		}

		entry, found := repository.ArtifactEntryFor(build.ArtifactName(inputName))
		if !found {
			if !input.Optional {
				missingRequiredInputs = append(missingRequiredInputs, inputName)
			}
			continue
		}
		if entry.Snapshot != nil {
			return nil, snapshotInputBindings{}, fmt.Errorf("task input %q is a typed snapshot but has no input_types declaration", inputName)
		}
		inputs = append(inputs, runtime.Input{
			Artifact:        entry.Artifact,
			DestinationPath: artifactPath(metadata.WorkingDirectory, input.Name, input.Path),
			FromCache:       entry.FromCache,
			ReadOnly:        step.readOnlyInput(inputName),
		})
	}

	if len(missingRequiredInputs) > 0 {
		return nil, snapshotInputBindings{}, MissingInputsError{missingRequiredInputs}
	}

	return inputs, bindings, nil
}

func (step *TaskStep) readOnlyInput(name string) bool {
	_, found := step.plan.ReadOnlyInputs[name]
	if found {
		return true
	}
	if authority := step.plan.DevValidationAuthority; authority != nil {
		if name == authority.CandidateInput {
			return true
		}
		for _, base := range authority.BaseInputs {
			if name == base.Name {
				return true
			}
		}
	}
	return false
}

func (step *TaskStep) containerSpec(logger lager.Logger, state RunState, imageSpec runtime.ImageSpec, config atc.TaskConfig, metadata db.ContainerMetadata) (runtime.ContainerSpec, snapshotInputBindings, error) {
	env := step.metadata.TaskEnv()
	env = append(env, config.Params.Env()...)

	containerSpec := runtime.ContainerSpec{
		TeamID:   step.metadata.TeamID,
		TeamName: step.metadata.TeamName,
		JobID:    step.metadata.JobID,
		StepName: step.plan.Name,

		ImageSpec: imageSpec,
		Env:       env,
		Type:      metadata.Type,

		Dir:      metadata.WorkingDirectory,
		Hermetic: step.plan.Hermetic,
	}

	var err error
	var bindings snapshotInputBindings
	containerSpec.Inputs, bindings, err = step.containerInputs(logger, state.ArtifactRepository(), config, metadata)
	if err != nil {
		return runtime.ContainerSpec{}, snapshotInputBindings{}, err
	}
	for _, input := range containerSpec.Inputs {
		if !input.ReadOnly {
			continue
		}
		for _, output := range config.Outputs {
			outputPath := ensureTrailingSlash(artifactPath(metadata.WorkingDirectory, output.Name, output.Path))
			if taskPathsOverlap(input.DestinationPath, outputPath) {
				return runtime.ContainerSpec{}, snapshotInputBindings{}, fmt.Errorf("task %q read-only input %q overlaps output %q", step.plan.Name, input.DestinationPath, output.Name)
			}
		}
	}

	outputTypes := make(map[string]snapshot.TypeRef, len(step.plan.SnapshotOutputs))
	for _, output := range config.Outputs {
		effectiveName := output.Name
		if mapped, found := step.plan.OutputMapping[output.Name]; found {
			effectiveName = mapped
		}
		if declaration, typed := step.plan.SnapshotOutputs[effectiveName]; typed {
			outputTypes[output.Name] = declaration.Type
		}
	}
	authorityEnv, err := recordAuthorityEnv(bindings, outputTypes, containerSpec.Env)
	if err != nil {
		return runtime.ContainerSpec{}, snapshotInputBindings{}, fmt.Errorf("task %q: %w", step.plan.Name, err)
	}
	containerSpec.Env = append(containerSpec.Env, authorityEnv...)

	containerSpec.Caches = make([]string, len(config.Caches))
	for i, cache := range config.Caches {
		containerSpec.Caches[i] = cache.Path
	}

	containerSpec.ScratchPaths = make([]string, len(config.ScratchPaths))
	for i, scratch := range config.ScratchPaths {
		containerSpec.ScratchPaths[i] = scratch.Path
	}

	containerSpec.Outputs = make(runtime.OutputPaths, len(config.Outputs))
	for _, output := range config.Outputs {
		containerSpec.Outputs[output.Name] = ensureTrailingSlash(artifactPath(metadata.WorkingDirectory, output.Name, output.Path))
	}
	optionalOutputNames := make([]string, 0, len(step.plan.SnapshotOutputs))
	for _, output := range config.Outputs {
		effectiveName := output.Name
		if mapped, found := step.plan.OutputMapping[output.Name]; found {
			effectiveName = mapped
		}
		if declaration, typed := step.plan.SnapshotOutputs[effectiveName]; typed && declaration.Optional {
			optionalOutputNames = append(optionalOutputNames, output.Name)
		}
	}
	if err := configureOptionalOutputMarkers(&containerSpec, optionalOutputNames); err != nil {
		return runtime.ContainerSpec{}, snapshotInputBindings{}, fmt.Errorf("task %q: %w", step.plan.Name, err)
	}

	if config.Limits != nil {
		containerSpec.Limits.CPU = (*uint64)(config.Limits.CPU)
		containerSpec.Limits.Memory = (*uint64)(config.Limits.Memory)
		containerSpec.Limits.EphemeralStorage = (*uint64)(config.Limits.EphemeralStorage)
	}
	if config.Requests != nil {
		containerSpec.Limits.CPURequest = (*uint64)(config.Requests.CPU)
		containerSpec.Limits.MemoryRequest = (*uint64)(config.Requests.Memory)
		containerSpec.Limits.EphemeralStorageRequest = (*uint64)(config.Requests.EphemeralStorage)
	}

	// Build SecretEnv: for params that were resolved from a K8s Secrets
	// backend, record the secret ref so the pod builder can use
	// ValueFrom.SecretKeyRef instead of a literal value.
	containerSpec.SecretEnv = BuildSecretEnv(config.Params, state)

	return containerSpec, bindings, nil
}

func taskPathsOverlap(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	for _, pair := range [][2]string{{left, right}, {right, left}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && (relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))) {
			return true
		}
	}
	return false
}

// BuildSecretEnv cross-references resolved param values against tracked
// secret refs. For each param whose resolved value matches a tracked
// credential that has a K8s Secret ref, the env var name is mapped to
// the secret ref.
func BuildSecretEnv(params atc.TaskEnv, state RunState) map[string]vars.SecretRef {
	// Collect tracked credentials: varPath -> value
	credValues := vars.TrackedVarsMap{}
	state.IterateInterpolatedCreds(credValues)

	// Collect tracked secret refs: varPath -> SecretRef
	secretRefs := map[string]vars.SecretRef{}
	state.IterateSecretRefs(secretRefCollector(secretRefs))

	if len(secretRefs) == 0 {
		return nil
	}

	// Build reverse map: credential value -> SecretRef
	valueToRef := map[string]vars.SecretRef{}
	for varPath, ref := range secretRefs {
		if val, ok := credValues[varPath]; ok {
			valueToRef[val] = ref
		}
	}

	if len(valueToRef) == 0 {
		return nil
	}

	// For each param env var, check if its value matches a tracked secret
	result := map[string]vars.SecretRef{}
	for name, value := range params {
		if ref, ok := valueToRef[value]; ok {
			result[name] = ref
		}
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

type secretRefCollector map[string]vars.SecretRef

func (c secretRefCollector) YieldSecretRef(varPath string, ref vars.SecretRef) {
	c[varPath] = ref
}

func (step *TaskStep) workerSpec(config atc.TaskConfig) worker.Spec {
	return worker.Spec{
		TeamID: step.metadata.TeamID,
	}
}

func (step *TaskStep) registerLegacyOutputs(logger lager.Logger, repository *build.Repository, worker runtime.Worker, config atc.TaskConfig, volumeMounts []runtime.VolumeMount, metadata db.ContainerMetadata) {
	logger.Debug("registering-outputs", lager.Data{"outputs": config.Outputs})

	for _, output := range config.Outputs {
		outputName := output.Name
		if destinationName, ok := step.plan.OutputMapping[output.Name]; ok {
			outputName = destinationName
		}
		if _, typed := step.plan.SnapshotOutputs[outputName]; typed {
			continue
		}

		outputPath := artifactPath(metadata.WorkingDirectory, output.Name, output.Path)

		for _, mount := range volumeMounts {
			if filepath.Clean(mount.MountPath) == filepath.Clean(outputPath) {
				// Wrap the container-mount volume as a DaemonSet-backed
				// Artifact reference before handing it to the repository.
				// Without this wrap, downstream consumers would exec into
				// the producing pod for StreamOut, which breaks once the
				// reaper deletes the pod (see forge/tracks/route_artifact_reads_through_daemonset_remove_exec_backed_artifact_io_20260418).
				artifact := worker.ArtifactFromVolume(mount.Volume)
				repository.RegisterArtifact(build.ArtifactName(outputName), artifact, false)
			}
		}
	}
}

type collectedTypedOutput struct {
	source snapshot.OutputSource
}

func (step *TaskStep) sealTypedOutputs(
	ctx context.Context,
	repository *build.Repository,
	worker runtime.Worker,
	config atc.TaskConfig,
	volumeMounts []runtime.VolumeMount,
	inputs snapshotInputBindings,
) error {
	outputs, declarations, workflowDefinitionID, workflowRunID, err := step.collectTypedOutputs(ctx, worker, config, volumeMounts)
	if err != nil {
		return err
	}
	sources := make([]snapshot.OutputSource, len(outputs))
	for i, output := range outputs {
		sources[i] = output.source
	}
	authorities, err := step.validationAuthorities(inputs, declarations)
	if err != nil {
		return err
	}
	sealed, err := step.outputSealer.Seal(ctx, snapshot.SealRequest{
		BuildID: step.metadata.BuildID, TeamID: step.metadata.TeamID, TeamName: step.metadata.TeamName,
		CreatedBy: step.metadata.SnapshotCreatedBy, PlanID: string(step.planID), Attempt: step.containerMetadata.Attempt,
		StepKind: "task", StepName: step.plan.Name,
		WorkflowDefinitionID: workflowDefinitionID, WorkflowRunID: workflowRunID,
		InputOrder: append([]string(nil), inputs.order...), Inputs: cloneExecSnapshotRefs(inputs.refs),
		InputExposures:                   cloneExecInputExposures(inputs.exposures),
		ValidationAttestationAuthorities: authorities,
		OutputDeclarations:               declarations, Outputs: sources,
	})
	if err != nil {
		return err
	}
	if len(sealed) != len(outputs) {
		return fmt.Errorf("task output sealer returned %d outputs, want %d", len(sealed), len(outputs))
	}
	entries := make(map[build.ArtifactName]build.ArtifactEntry, len(outputs))
	for _, output := range outputs {
		sealedOutput, found := sealed[output.source.ClientKey]
		if !found {
			return fmt.Errorf("task output sealer did not return client key %q", output.source.ClientKey)
		}
		if err := sealedOutput.Validate(); err != nil {
			return fmt.Errorf("task output sealer returned invalid output %q: %w", output.source.ClientKey, err)
		}
		if sealedOutput.Port != output.source.Port {
			return fmt.Errorf("task output sealer returned a mismatched declaration for %q", output.source.ClientKey)
		}
		ref := sealedOutput.Snapshot
		artifact, err := materializeSealedSnapshotArtifact(
			ctx, step.metadata.TeamID, ref, step.snapshotMetadataStore, step.snapshotContentStore,
		)
		if err != nil {
			return fmt.Errorf("task sealed output %q: %w", output.source.ClientKey, err)
		}
		entries[build.ArtifactName(output.source.ClientKey)] = build.ArtifactEntry{Artifact: artifact, Snapshot: &ref}
	}
	if err := repository.RegisterArtifacts(entries); err != nil {
		return fmt.Errorf("publish typed task outputs: %w", err)
	}
	return nil
}

func (step *TaskStep) validationAuthorities(inputs snapshotInputBindings, declarations []snapshot.Port) (map[string]snapshot.ValidationAttestationAuthority, error) {
	a := step.plan.DevValidationAuthority
	for _, declaration := range declarations {
		if declaration.Type == snapshot.TypeRef("validation/v1") && a == nil {
			return nil, fmt.Errorf("task %q validation output lacks server authority", step.plan.Name)
		}
	}
	if a == nil {
		return nil, nil
	}
	if step.metadata.WorkflowDefinitionID == nil || *step.metadata.WorkflowDefinitionID != a.WorkflowDefinitionID {
		return nil, fmt.Errorf("task %q validation authority does not match workflow", step.plan.Name)
	}
	candidate, ok := inputs.refs[a.CandidateInput]
	if !ok {
		return nil, fmt.Errorf("task %q validation candidate is not bound", step.plan.Name)
	}
	if declaration, found := step.plan.SnapshotInputs[a.CandidateInput]; !found || candidate.Type != declaration.Type {
		return nil, fmt.Errorf("task %q validation candidate type is not bound exactly", step.plan.Name)
	}
	bases := make([]snapshot.ValidationAuthorityInput, 0, len(a.BaseInputs))
	for _, base := range a.BaseInputs {
		ref, ok := inputs.refs[base.Name]
		if !ok || ref.Type != base.Type {
			return nil, fmt.Errorf("task %q validation base %q is not bound", step.plan.Name, base.Name)
		}
		bases = append(bases, snapshot.ValidationAuthorityInput{Input: base.Name, Ref: ref})
	}
	sort.Slice(bases, func(i, j int) bool { return bases[i].Input < bases[j].Input })
	authority := snapshot.ValidationAttestationAuthority{CandidateInput: a.CandidateInput, Candidate: candidate, BaseInputs: bases, ProfileDigest: a.ProfileDigest, ProtectedConfigDigest: a.ProtectedConfigDigest, CapabilityImage: a.CapabilityImage, CapabilityImageDigest: a.CapabilityImageDigest, WorkflowDefinitionID: a.WorkflowDefinitionID, WorkflowVersion: a.WorkflowVersion, Toolchain: "dev-capability/" + a.CapabilityImageDigest.String()}
	if err := authority.Validate(); err != nil {
		return nil, err
	}
	return map[string]snapshot.ValidationAttestationAuthority{atc.DevValidationOutput: authority}, nil
}

// validateDevValidationTask accepts only the renderer-owned task shape. It is
// intentionally repeated at execution: private plans can be resumed and must
// not gain authority merely by having once passed source compilation.
func (step *TaskStep) validateDevValidationTask(config atc.TaskConfig) error {
	a := step.plan.DevValidationAuthority
	for name, output := range step.plan.SnapshotOutputs {
		if output.Type == snapshot.TypeRef("validation/v1") && a == nil {
			return fmt.Errorf("task %q validation output %q lacks server authority", step.plan.Name, name)
		}
	}
	if a == nil {
		return nil
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("task %q validation authority: %w", step.plan.Name, err)
	}
	if step.metadata.WorkflowDefinitionID == nil || step.metadata.WorkflowRunID == nil || *step.metadata.WorkflowDefinitionID != a.WorkflowDefinitionID {
		return fmt.Errorf("task %q validation authority does not match authenticated workflow", step.plan.Name)
	}
	if step.plan.FunctionID != "dev-validation-"+a.ProfileName || step.plan.Privileged || !step.plan.Hermetic || step.plan.ConfigPath != "" || step.plan.ImageArtifactName != "" || step.plan.Timeout != "" || len(step.plan.Params) != 0 || len(step.plan.Vars) != 0 || len(step.plan.Tags) != 0 || len(step.plan.Sidecars) != 0 || len(step.plan.InputMapping) != 0 || len(step.plan.OutputMapping) != 0 || step.plan.Limits != nil || step.plan.Requests != nil {
		return fmt.Errorf("task %q authoritative validation has authored controls", step.plan.Name)
	}
	if config.ImageResource != nil || config.RootfsURI != a.CapabilityImage || config.Platform != "linux" || len(config.Params) != 0 || len(config.Caches) != 0 || len(config.Inputs) != 1+len(a.BaseInputs) || len(config.Outputs) != 1 || len(config.ScratchPaths) != 1 || config.ScratchPaths[0].Path != atc.DevValidationWorkspacePath || config.Run.Path != atc.DevValidationFunctionRunnerPath || config.Run.Dir != "" || config.Run.User != "" {
		return fmt.Errorf("task %q authoritative validation is not the fixed task shape", step.plan.Name)
	}
	candidate, found := step.plan.SnapshotInputs[a.CandidateInput]
	if !found || candidate.Optional || len(step.plan.SnapshotInputs) != 1+len(a.BaseInputs) || len(step.plan.SnapshotOutputs) != 1 {
		return fmt.Errorf("task %q authoritative validation has invalid typed ports", step.plan.Name)
	}
	if output, found := step.plan.SnapshotOutputs[atc.DevValidationOutput]; !found || output.Optional || output.Type != snapshot.TypeRef("validation/v1") {
		return fmt.Errorf("task %q authoritative validation must emit validation/v1", step.plan.Name)
	}
	if config.Outputs[0].Name != atc.DevValidationOutput || config.Outputs[0].Path != "" {
		return fmt.Errorf("task %q authoritative validation has invalid output mount", step.plan.Name)
	}
	if !equalTaskStrings(config.Run.Args, atc.DevValidationStaticArgs(*a, candidate.Type)) {
		return fmt.Errorf("task %q authoritative validation arguments are not fixed", step.plan.Name)
	}
	for index, input := range config.Inputs {
		if input.Optional || input.Path != "" || (index == 0 && input.Name != a.CandidateInput) {
			return fmt.Errorf("task %q authoritative validation has invalid input mounts", step.plan.Name)
		}
	}
	for _, base := range a.BaseInputs {
		input, found := step.plan.SnapshotInputs[base.Name]
		if !found || input.Optional || input.Type != base.Type {
			return fmt.Errorf("task %q authoritative validation has invalid base %q", step.plan.Name, base.Name)
		}
	}
	return nil
}

func equalTaskStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (step *TaskStep) bindDevValidationCandidate(config *atc.TaskConfig, inputs snapshotInputBindings) error {
	a := step.plan.DevValidationAuthority
	if a == nil {
		return nil
	}
	candidate, found := inputs.refs[a.CandidateInput]
	if !found {
		return fmt.Errorf("task %q validation candidate is not bound", step.plan.Name)
	}
	if err := candidate.Validate(); err != nil {
		return fmt.Errorf("task %q validation candidate: %w", step.plan.Name, err)
	}
	config.Run.Args = append(append([]string(nil), config.Run.Args...), "--candidate-id", candidate.ID.String(), "--candidate-digest", candidate.Digest.String())
	for _, base := range a.BaseInputs {
		ref := inputs.refs[base.Name]
		config.Run.Args = append(config.Run.Args, "--base-ref", fmt.Sprintf("%s=%s,%s,%s", base.Name, ref.ID, ref.Type, ref.Digest))
	}
	return nil
}

func (step *TaskStep) addDevValidationAuthorityInput(spec *runtime.ContainerSpec) error {
	a := step.plan.DevValidationAuthority
	if a == nil {
		return nil
	}
	if err := a.Validate(); err != nil {
		return fmt.Errorf("task %q validation authority: %w", step.plan.Name, err)
	}
	// An artifact volume would have to be hydrated by a privileged init
	// container. Keep the profile/config out of the artifact transport
	// altogether; Jetbridge materializes this server-owned mount as a
	// main-container-only Kubernetes Secret.
	spec.PrivateFileMounts = append(spec.PrivateFileMounts, runtime.PrivateFileMount{
		MountPath: atc.DevValidationProtectedRoot,
		Files: map[string][]byte{
			"config.yml":  append([]byte(nil), a.ProtectedConfig...),
			"profile.yml": append([]byte(nil), a.Profile...),
		},
	})
	return nil
}

func (step *TaskStep) collectTypedOutputs(
	ctx context.Context,
	worker runtime.Worker,
	config atc.TaskConfig,
	volumeMounts []runtime.VolumeMount,
) ([]collectedTypedOutput, []snapshot.Port, *int, *snapshot.WorkflowRunID, error) {
	outputs := make([]collectedTypedOutput, 0, len(step.plan.SnapshotOutputs))
	declarations := make([]snapshot.Port, 0, len(step.plan.SnapshotOutputs))
	seen := make(map[string]struct{}, len(step.plan.SnapshotOutputs))
	workflowDefinitionID, workflowRunID, err := snapshotWorkflowAssociation(
		"task",
		step.plan.SnapshotOutputs,
		step.metadata.WorkflowDefinitionID,
		step.metadata.WorkflowRunID,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	markerArtifact, err := optionalOutputMarkerArtifact(worker, volumeMounts, hasOptionalSnapshotOutputs(step.plan.SnapshotOutputs))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("task: %w", err)
	}
	for _, output := range config.Outputs {
		effectiveName := output.Name
		if mapped, found := step.plan.OutputMapping[output.Name]; found {
			effectiveName = mapped
		}
		declaration, typed := step.plan.SnapshotOutputs[effectiveName]
		if !typed {
			continue
		}
		if _, duplicate := seen[effectiveName]; duplicate {
			return nil, nil, nil, nil, fmt.Errorf("task typed output %q is declared more than once", effectiveName)
		}
		seen[effectiveName] = struct{}{}
		port := snapshot.Port{Name: effectiveName, Type: declaration.Type, Optional: declaration.Optional}
		declarations = append(declarations, port)
		outputPath := artifactPath(step.containerMetadata.WorkingDirectory, output.Name, output.Path)
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
			return nil, nil, nil, nil, fmt.Errorf("task typed output %q has duplicate mounts", effectiveName)
		}
		if matches == 0 {
			return nil, nil, nil, nil, fmt.Errorf("task required typed output %q has no mount", effectiveName)
		}
		if declaration.Optional {
			produced, err := optionalOutputWasProduced(ctx, markerArtifact, output.Name)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("task optional typed output %q: %w", effectiveName, err)
			}
			if !produced {
				continue
			}
		}
		capturedArtifact := artifact
		outputs = append(outputs, collectedTypedOutput{
			source: snapshot.OutputSource{
				ClientKey: effectiveName, Port: port, Retention: declaration.Retention,
				WorkflowPort:   declaration.WorkflowPort,
				SourceMetadata: append(json.RawMessage(nil), declaration.SourceMetadata...),
				OpenTar: func(ctx context.Context) (io.ReadCloser, error) {
					return capturedArtifact.StreamOut(ctx, ".", nil)
				},
			},
		})
	}
	return outputs, declarations, workflowDefinitionID, workflowRunID, nil
}

func (step *TaskStep) validateSnapshotDeclarations(config atc.TaskConfig) error {
	inputs := make(map[string]struct{}, len(config.Inputs))
	for _, input := range config.Inputs {
		name := input.Name
		if mapped, found := step.plan.InputMapping[input.Name]; found {
			name = mapped
		}
		inputs[name] = struct{}{}
	}
	for _, name := range sortedSnapshotKeys(step.plan.SnapshotInputs) {
		declaration := step.plan.SnapshotInputs[name]
		if err := declaration.Validate(); err != nil {
			return fmt.Errorf("task typed input %q: %w", name, err)
		}
		if _, found := inputs[name]; !found {
			return fmt.Errorf("task typed input %q is absent from the fetched task config after input mapping", name)
		}
	}
	outputs := make(map[string]struct{}, len(config.Outputs))
	for _, output := range config.Outputs {
		name := output.Name
		if mapped, found := step.plan.OutputMapping[output.Name]; found {
			name = mapped
		}
		outputs[name] = struct{}{}
	}
	for _, name := range sortedSnapshotKeys(step.plan.SnapshotOutputs) {
		if _, found := outputs[name]; !found {
			return fmt.Errorf("task typed output %q is absent from the fetched task config after output mapping", name)
		}
	}
	if len(step.plan.SnapshotInputs) > 0 || len(step.plan.SnapshotOutputs) > 0 {
		if err := validateExactArtifactCoverage("task", "input", inputs, sortedSnapshotKeys(step.plan.SnapshotInputs)); err != nil {
			return err
		}
		if err := validateExactArtifactCoverage("task", "output", outputs, sortedSnapshotKeys(step.plan.SnapshotOutputs)); err != nil {
			return err
		}
	}
	_, _, err := snapshotWorkflowAssociation(
		"task",
		step.plan.SnapshotOutputs,
		step.metadata.WorkflowDefinitionID,
		step.metadata.WorkflowRunID,
	)
	return err
}

func validateExactArtifactCoverage(kind, direction string, ordinary map[string]struct{}, typed []string) error {
	declared := sortedArtifactSetKeys(ordinary)
	if len(declared) != len(typed) {
		return fmt.Errorf("every declared %s %s must be typed: declared=%v typed=%v", kind, direction, declared, typed)
	}
	for index := range declared {
		if declared[index] != typed[index] {
			return fmt.Errorf("every declared %s %s must be typed: declared=%v typed=%v", kind, direction, declared, typed)
		}
	}
	return nil
}

func sortedArtifactSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// cloneExecInputExposures copies the mount-time exposure lineage into the seal
// request so the sealer cannot be handed a map the step still mutates.
func cloneExecInputExposures(exposures map[string]snapshot.InputExposure) map[string]snapshot.InputExposure {
	if len(exposures) == 0 {
		return nil
	}
	cloned := make(map[string]snapshot.InputExposure, len(exposures))
	for port, exposure := range exposures {
		cloned[port] = exposure.Clone()
	}
	return cloned
}

func cloneExecSnapshotRefs(refs map[string]snapshot.SnapshotRef) map[string]snapshot.SnapshotRef {
	cloned := make(map[string]snapshot.SnapshotRef, len(refs))
	for name, ref := range refs {
		cloned[name] = ref
	}
	return cloned
}

func snapshotWorkflowAssociation(
	stepKind string,
	outputs map[string]atc.SnapshotOutputConfig,
	authenticatedWorkflowDefinitionID *int,
	authenticatedWorkflowRunID *snapshot.WorkflowRunID,
) (*int, *snapshot.WorkflowRunID, error) {
	var claimedWorkflowDefinitionID *int
	var claimedWorkflowRunID *snapshot.WorkflowRunID
	for _, name := range sortedSnapshotKeys(outputs) {
		declaration := outputs[name]
		if err := declaration.Validate(); err != nil {
			return nil, nil, fmt.Errorf("%s typed output %q: %w", stepKind, name, err)
		}
		if declaration.Retention != snapshot.RetentionClassWorkflow {
			continue
		}
		parsedRunID, err := snapshot.ParseWorkflowRunID(declaration.WorkflowRunID)
		if err != nil {
			return nil, nil, fmt.Errorf("%s typed output %q workflow run ID: %w", stepKind, name, err)
		}
		if claimedWorkflowDefinitionID == nil {
			definitionID := declaration.WorkflowDefinitionID
			runID := parsedRunID
			claimedWorkflowDefinitionID = &definitionID
			claimedWorkflowRunID = &runID
			continue
		}
		if *claimedWorkflowDefinitionID != declaration.WorkflowDefinitionID || *claimedWorkflowRunID != parsedRunID {
			return nil, nil, fmt.Errorf("%s typed outputs claim multiple workflow definition/run associations", stepKind)
		}
	}
	if (authenticatedWorkflowDefinitionID == nil) != (authenticatedWorkflowRunID == nil) {
		return nil, nil, fmt.Errorf("%s authenticated workflow association is incomplete", stepKind)
	}
	if authenticatedWorkflowDefinitionID == nil {
		if claimedWorkflowDefinitionID != nil {
			return nil, nil, fmt.Errorf(
				"%s typed output claims workflow retention but its build is not associated with a workflow run",
				stepKind,
			)
		}
		return nil, nil, nil
	}
	if *authenticatedWorkflowDefinitionID <= 0 {
		return nil, nil, fmt.Errorf("%s authenticated workflow definition ID must be positive", stepKind)
	}
	if err := authenticatedWorkflowRunID.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%s authenticated workflow run ID: %w", stepKind, err)
	}
	if claimedWorkflowDefinitionID != nil &&
		(*claimedWorkflowDefinitionID != *authenticatedWorkflowDefinitionID ||
			*claimedWorkflowRunID != *authenticatedWorkflowRunID) {
		return nil, nil, fmt.Errorf(
			"%s typed output workflow association does not match its authenticated build",
			stepKind,
		)
	}
	definitionID := *authenticatedWorkflowDefinitionID
	runID := *authenticatedWorkflowRunID
	return &definitionID, &runID, nil
}

func sortedSnapshotKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (step *TaskStep) registerCaches(ctx context.Context, repository *build.Repository, config atc.TaskConfig, volumeMounts []runtime.VolumeMount, metadata db.ContainerMetadata) error {
	logger := lagerctx.FromContext(ctx)
	for _, cacheConfig := range config.Caches {
		for _, volumeMount := range volumeMounts {
			mountPath := resolvePath(metadata.WorkingDirectory, cacheConfig.Path)
			if filepath.Clean(volumeMount.MountPath) == mountPath {
				logger.Debug("initializing-cache", lager.Data{
					"cache": cacheConfig.Path,
				})
				err := volumeMount.Volume.InitializeTaskCache(
					ctx,
					step.metadata.JobID,
					step.plan.Name,
					cacheConfig.Path,
					step.plan.Privileged,
				)
				if err != nil {
					return err
				}

				break
			}
		}
	}

	return nil
}

type ociRunConfig struct {
	Cmd        []string `json:"cmd"`
	EntryPoint []string `json:"entrypoint"`
}

// Try and use OCI image Entrypoint and/or CMD. This relies on the
// registry-image resource providing this information when it converts OCI
// images into our rootfs format.
func (step *TaskStep) getOciEntrypoint(ctx context.Context, imageSpec runtime.ImageSpec) (string, []string, error) {
	path := ""
	args := []string{}

	if imageSpec.ImageArtifact == nil {
		// there's no image artifact for Windows and Darwin tasks
		return path, args, nil
	}

	metadataStream, err := step.streamer.StreamFile(ctx, imageSpec.ImageArtifact, "metadata.json")
	if err != nil {
		if err == runtime.ErrFileNotFound {
			return path, args, FileNotFoundError{
				Name:     imageSpec.ImageArtifact.Handle(),
				FilePath: "metadata.json",
			}
		}
		return path, args, err
	}

	defer metadataStream.Close()

	imageMetadata, err := io.ReadAll(metadataStream)
	if err != nil {
		return path, args, err
	}

	imgCmd := ociRunConfig{}
	err = json.Unmarshal(imageMetadata, &imgCmd)
	if err != nil {
		return path, args, fmt.Errorf("error parsing metadata.json from rootfs: %s", err.Error())
	}

	if len(imgCmd.EntryPoint) >= 1 {
		path = imgCmd.EntryPoint[0]
	}

	if len(imgCmd.EntryPoint) > 1 {
		args = append(args, imgCmd.EntryPoint[1:]...)
	}

	if path == "" && len(imgCmd.Cmd) >= 1 {
		path = imgCmd.Cmd[0]
		if len(imgCmd.Cmd) > 1 {
			args = append(args, imgCmd.Cmd[1:]...)
		}
	} else {
		args = append(args, imgCmd.Cmd...)
	}

	return path, args, err
}

func artifactPath(workingDir string, name string, path string) string {
	subdir := path
	if path == "" {
		subdir = name
	}

	return resolvePath(workingDir, subdir)
}

func resolvePath(workingDir string, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	path = strings.ReplaceAll(path, "..", "")
	return filepath.Join(workingDir, path)
}

func ensureTrailingSlash(path string) string {
	if strings.HasSuffix(path, "/") {
		return path
	}
	return path + "/"
}

// stripDockerPrefix removes Concourse-internal URL prefixes from an image
// reference so it can be parsed as a bare Docker image ref.
func stripDockerPrefix(image string) string {
	for _, prefix := range []string{"docker:///", "docker://", "raw:///"} {
		if strings.HasPrefix(image, prefix) {
			return strings.TrimPrefix(image, prefix)
		}
	}
	return image
}

// parseImageRef splits an image reference like "redis:7" or "myrepo/myimage"
// into repository and tag components. If no tag is specified, defaults to "latest".
func parseImageRef(image string) (string, string) {
	// Handle images with ports in registry (e.g. "registry:5000/repo:tag")
	lastColon := strings.LastIndex(image, ":")
	lastSlash := strings.LastIndex(image, "/")

	if lastColon > lastSlash && lastColon > 0 {
		return image[:lastColon], image[lastColon+1:]
	}
	return image, "latest"
}
