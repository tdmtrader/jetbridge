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

// TaskStep executes a TaskConfig, whose inputs will be fetched from the
// artifact.Repository and outputs will be added to the artifact.Repository.
type TaskStep struct {
	planID             atc.PlanID
	plan               atc.TaskPlan
	defaultLimits      atc.ContainerLimits
	defaultRequests    atc.ContainerLimits
	metadata           StepMetadata
	containerMetadata  db.ContainerMetadata
	workerPool         Pool
	streamer           Streamer
	delegateFactory    TaskDelegateFactory
	defaultTaskTimeout time.Duration
	imageResolver      imageresolver.Resolver
	outputSealer       snapshot.OutputSealer
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
	if (len(step.plan.SnapshotInputs) > 0 || len(step.plan.SnapshotOutputs) > 0) && step.outputSealer == nil {
		return false, fmt.Errorf("task %q has typed snapshot declarations but no output sealer is configured", step.plan.Name)
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
		artifact, _, found := state.ArtifactRepository().ArtifactFor(build.ArtifactName(step.plan.ImageArtifactName))
		if !found {
			return runtime.ImageSpec{}, MissingTaskImageSourceError{step.plan.ImageArtifactName}
		}
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
			inputs = append(inputs, runtime.Input{
				Artifact: entry.Artifact, DestinationPath: artifactPath(metadata.WorkingDirectory, input.Name, input.Path),
				FromCache: entry.FromCache,
			})
			bindings.order = append(bindings.order, inputName)
			bindings.refs[inputName] = ref
			continue
		}

		artifact, fromCache, found := repository.ArtifactFor(build.ArtifactName(inputName))
		if !found {
			if !input.Optional {
				missingRequiredInputs = append(missingRequiredInputs, inputName)
			}
			continue
		}
		inputs = append(inputs, runtime.Input{
			Artifact:        artifact,
			DestinationPath: artifactPath(metadata.WorkingDirectory, input.Name, input.Path),
			FromCache:       fromCache,
		})
	}

	if len(missingRequiredInputs) > 0 {
		return nil, snapshotInputBindings{}, MissingInputsError{missingRequiredInputs}
	}

	return inputs, bindings, nil
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
	source   snapshot.OutputSource
	artifact runtime.Artifact
}

func (step *TaskStep) sealTypedOutputs(
	ctx context.Context,
	repository *build.Repository,
	worker runtime.Worker,
	config atc.TaskConfig,
	volumeMounts []runtime.VolumeMount,
	inputs snapshotInputBindings,
) error {
	outputs, declarations, workflowDefinitionID, workflowRunID, err := step.collectTypedOutputs(worker, config, volumeMounts)
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
		StepKind: "task", StepName: step.plan.Name,
		WorkflowDefinitionID: workflowDefinitionID, WorkflowRunID: workflowRunID,
		InputOrder: append([]string(nil), inputs.order...), Inputs: cloneExecSnapshotRefs(inputs.refs),
		OutputDeclarations: declarations, Outputs: sources,
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
		entries[build.ArtifactName(output.source.ClientKey)] = build.ArtifactEntry{Artifact: output.artifact, Snapshot: &ref}
	}
	if err := repository.RegisterArtifacts(entries); err != nil {
		return fmt.Errorf("publish typed task outputs: %w", err)
	}
	return nil
}

func (step *TaskStep) collectTypedOutputs(
	worker runtime.Worker,
	config atc.TaskConfig,
	volumeMounts []runtime.VolumeMount,
) ([]collectedTypedOutput, []snapshot.Port, *int, *snapshot.WorkflowRunID, error) {
	outputs := make([]collectedTypedOutput, 0, len(step.plan.SnapshotOutputs))
	declarations := make([]snapshot.Port, 0, len(step.plan.SnapshotOutputs))
	seen := make(map[string]struct{}, len(step.plan.SnapshotOutputs))
	workflowDefinitionID, workflowRunID, err := snapshotWorkflowAssociation("task", step.plan.SnapshotOutputs)
	if err != nil {
		return nil, nil, nil, nil, err
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
			if declaration.Optional {
				continue
			}
			return nil, nil, nil, nil, fmt.Errorf("task required typed output %q has no mount", effectiveName)
		}
		capturedArtifact := artifact
		outputs = append(outputs, collectedTypedOutput{
			artifact: artifact,
			source: snapshot.OutputSource{
				ClientKey: effectiveName, Port: port, Retention: declaration.Retention,
				WorkflowPort: declaration.WorkflowPort,
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
	_, _, err := snapshotWorkflowAssociation("task", step.plan.SnapshotOutputs)
	return err
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
) (*int, *snapshot.WorkflowRunID, error) {
	var workflowDefinitionID *int
	var workflowRunID *snapshot.WorkflowRunID
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
		if workflowDefinitionID == nil {
			definitionID := declaration.WorkflowDefinitionID
			runID := parsedRunID
			workflowDefinitionID = &definitionID
			workflowRunID = &runID
			continue
		}
		if *workflowDefinitionID != declaration.WorkflowDefinitionID || *workflowRunID != parsedRunID {
			return nil, nil, fmt.Errorf("%s typed outputs claim multiple workflow definition/run associations", stepKind)
		}
	}
	return workflowDefinitionID, workflowRunID, nil
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
