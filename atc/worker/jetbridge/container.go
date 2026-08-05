package jetbridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	exitStatusPropertyName       = "concourse:exit-status"
	resourceResultPropertyName   = "concourse:resource-result"
	mainContainerName            = "main"
	exitStatusAnnotationKey      = "concourse.ci/exit-status"
	supervisorStateAnnotationKey = "concourse.ci/supervisor-state-dir"
	resourceResultAnnotationKey  = "concourse.ci/resource-result"
	privateMountSecretLabelKey   = "concourse.ci/private-mount-for"
	privateMountPodUIDLabelKey   = "concourse.ci/private-mount-pod-uid"
	privateMountPodNameLabelKey  = "concourse.ci/private-mount-pod-name"
	privateMountDataDigestKey    = "concourse.ci/private-mount-data-digest"
	privateMountRoot             = "/run/concourse"
	maxPrivateMountSecretBytes   = 1 << 20
	privateMountCreateCleanupTTL = 5 * time.Second
)

// persistableAnnotations maps container property keys to pod annotation keys
// for properties that should survive web restarts.
var persistableAnnotations = map[string]string{
	exitStatusPropertyName:     exitStatusAnnotationKey,
	resourceResultPropertyName: resourceResultAnnotationKey,
}

// Compile-time check that Container satisfies runtime.Container.
var _ runtime.Container = (*Container)(nil)

// Container implements runtime.Container backed by a Kubernetes Pod.
// The Pod is created lazily when Run() is called, since the command
// (ProcessSpec) isn't known at FindOrCreateContainer time.
type Container struct {
	handle           string
	podName          string
	metadata         db.ContainerMetadata
	containerSpec    runtime.ContainerSpec
	dbContainer      db.CreatedContainer
	clientset        kubernetes.Interface
	config           Config
	workerName       string
	mu               sync.RWMutex
	recoveryMu       sync.Mutex
	recoveryPodUID   types.UID
	recoveryNodeName string
	recoveryReady    bool
	boundaryMu       sync.Mutex
	boundaryActive   bool
	properties       map[string]string
	loadAnnotations  sync.Once
	executor         PodExecutor
	volumes          []*Volume
	storageBackend   *DaemonSetBackend
	// reused is true when FindOrCreateContainer found an existing container
	// in the DB (crash-recovery path). In DaemonSet mode this means the
	// hostPath directory may contain stale data and needs cleanup.
	reused bool
	// lookedUp is true when the Container was built by LookupContainer
	// (fly hijack/intercept). Such a Container has no ContainerSpec — it can
	// only attach to a pod some step already created, never build one.
	lookedUp bool
	// privateMountSecretNames are allocated afresh for each new Pod. They are
	// deliberately retained only long enough to put the exact names in that
	// Pod's spec. Trusted, immutable Secrets are created before that Pod is
	// exposed to Kubernetes, then bound to its UID with a CAS update.
	privateMountSecretNames   []string
	privateMountNameGenerator func() (string, error)
}

func newContainer(
	handle string,
	metadata db.ContainerMetadata,
	containerSpec runtime.ContainerSpec,
	dbContainer db.CreatedContainer,
	clientset kubernetes.Interface,
	config Config,
	workerName string,
	executor PodExecutor,
	volumes []*Volume,
	storageBackend *DaemonSetBackend,
	reused bool,
) *Container {
	if containerSpec.ManagedOutputBuilder != nil {
		builder := *containerSpec.ManagedOutputBuilder
		builder.Authority.Files = make(map[string][]byte, len(builder.Authority.Files))
		for name, contents := range containerSpec.ManagedOutputBuilder.Authority.Files {
			builder.Authority.Files[name] = append([]byte(nil), contents...)
		}
		builder.InputMountPaths = append([]string(nil), builder.InputMountPaths...)
		builder.OutputMountPaths = append([]string(nil), builder.OutputMountPaths...)
		containerSpec.ManagedOutputBuilder = &builder
	}
	if containerSpec.ManagedAgentBroker != nil {
		broker := *containerSpec.ManagedAgentBroker
		broker.Authority.Files = make(map[string][]byte, len(broker.Authority.Files))
		for name, contents := range containerSpec.ManagedAgentBroker.Authority.Files {
			broker.Authority.Files[name] = append([]byte(nil), contents...)
		}
		broker.Credentials = append([]runtime.SecretKeyMount(nil), broker.Credentials...)
		containerSpec.ManagedAgentBroker = &broker
	}
	return &Container{
		handle:         handle,
		podName:        GeneratePodName(metadata, handle),
		metadata:       metadata,
		containerSpec:  containerSpec,
		dbContainer:    dbContainer,
		clientset:      clientset,
		config:         config,
		workerName:     workerName,
		properties:     make(map[string]string),
		executor:       executor,
		volumes:        volumes,
		storageBackend: storageBackend,
		reused:         reused,
	}
}

func (c *Container) Run(ctx context.Context, spec runtime.ProcessSpec, io runtime.ProcessIO) (runtime.Process, error) {
	logger := lagerctx.FromContext(ctx).Session("container-run", lager.Data{
		"handle": c.handle,
	})

	execMode := c.executor != nil
	ctx, span := tracing.StartSpan(ctx, "k8s.container.run", tracing.Attrs{
		"handle":    c.handle,
		"image":     resolveImage(c.containerSpec.ImageSpec, c.config.ResourceTypeImages),
		"type":      string(c.containerSpec.Type),
		"namespace": c.config.Namespace,
		"exec-mode": fmt.Sprintf("%t", execMode),
		"build_id":  strconv.Itoa(c.metadata.BuildID),
		"pod_name":  c.podName,
	})
	var err error
	defer func() { tracing.End(span, err) }()

	processID := c.handle
	if spec.ID != "" {
		processID = spec.ID
	}

	// Exec mode: use a pause Pod and exec the real command via SPDY.
	// If the pod already exists and is Running (e.g. fly hijack or
	// repeated check on the same container handle), reuse it. If the
	// pod exists but is in a terminal state (Succeeded/Failed — e.g.
	// the pause pod's sleep expired or it was evicted), delete it and
	// create a fresh one. Otherwise create a new pause pod.
	if execMode {
		podName := c.podName
		existingPod, getErr := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
		needsCreate := getErr != nil // pod doesn't exist

		// A looked-up Container (fly hijack/intercept) carries no
		// ContainerSpec, so it must never fabricate or replace a pod:
		// building from the empty spec fails with a misleading "empty image
		// for resource type (unknown)" error, and replacing a completed pod
		// would destroy the exit-status annotation a restarted web needs to
		// resume the step. Say plainly that there is nothing to intercept.
		if c.lookedUp {
			if needsCreate {
				return nil, fmt.Errorf("container %q has no pod to intercept: pod %q does not exist", c.handle, c.podName)
			}
			if existingPod.Status.Phase == corev1.PodSucceeded || existingPod.Status.Phase == corev1.PodFailed {
				return nil, fmt.Errorf("container %q has no pod to intercept: pod %q already exited (%s)", c.handle, c.podName, existingPod.Status.Phase)
			}
		}

		if getErr == nil && (existingPod.Status.Phase == corev1.PodSucceeded || existingPod.Status.Phase == corev1.PodFailed) {
			// Pod exists but is terminal — delete it so we can create a fresh one.
			logger.Info("deleting-terminal-pod", lager.Data{
				"pod":   c.podName,
				"phase": string(existingPod.Status.Phase),
			})
			_ = c.clientset.CoreV1().Pods(c.config.Namespace).Delete(ctx, c.podName, metav1.DeleteOptions{})
			needsCreate = true
		}

		if needsCreate {
			var pod *corev1.Pod
			pod, err = c.createPausePod(ctx, spec)
			if err != nil {
				logger.Error("failed-to-create-pause-pod", err)
				metric.Metrics.FailedContainers.Inc()
				return nil, wrapIfTransient(fmt.Errorf("create pause pod: %w", err))
			}
			podName = pod.Name
		}
		metric.Metrics.ContainersCreated.Inc()
		c.bindVolumesToPod(podName)
		return newExecProcess(processID, podName, c.clientset, c.config, c, c.executor, spec, io, c.storageBackend), nil
	}

	// Fallback direct mode: only used when no executor is configured
	// (e.g. tests that don't set up SPDY). Bakes command into Pod spec.
	if c.lookedUp {
		return nil, fmt.Errorf("container %q cannot be intercepted: worker %q has no exec transport configured", c.handle, c.workerName)
	}
	var pod *corev1.Pod
	pod, err = c.createPod(ctx, spec)
	if err != nil {
		logger.Error("failed-to-create-pod", err)
		metric.Metrics.FailedContainers.Inc()
		return nil, wrapIfTransient(fmt.Errorf("create pod: %w", err))
	}
	metric.Metrics.ContainersCreated.Inc()
	c.bindVolumesToPod(pod.Name)

	return newProcess(processID, pod.Name, c.clientset, c.config, c, io), nil
}

// outputPaths returns the set of mount paths that should be uploaded to the
// artifact store after step completion. For task steps with explicit Outputs,
// only those paths are returned. For get/put steps (no explicit Outputs),
// the working directory is the implicit output and is included instead.
// When an output overlaps an input path, it is included because the step
// may have modified the input data.
//
// Paths are normalized via filepath.Clean so that trailing-slash differences
// between input paths (no slash) and output paths (trailing slash) don't
// cause mismatches when recording artifact locations.
func (c *Container) outputPaths() map[string]bool {
	if len(c.containerSpec.Outputs) > 0 {
		paths := make(map[string]bool, len(c.containerSpec.Outputs))
		for _, path := range c.containerSpec.Outputs {
			paths[filepath.Clean(path)] = true
		}
		return paths
	}
	// No explicit outputs — for get/put steps the working directory is the
	// implicit output. Task and check steps with no outputs don't produce
	// artifacts for downstream consumption.
	if c.containerSpec.Dir != "" &&
		c.metadata.Type != db.ContainerTypeTask &&
		c.metadata.Type != db.ContainerTypeCheck {
		return map[string]bool{c.containerSpec.Dir: true}
	}
	return nil
}

// volumeForPath returns the Volume associated with the given mount path,
// or nil if no matching volume is found.
func (c *Container) volumeForPath(mountPath string) *Volume {
	for _, v := range c.volumes {
		if v.MountPath() == mountPath {
			return v
		}
	}
	return nil
}

// bindVolumesToPod sets the pod name on all deferred volumes so that
// StreamIn/StreamOut can target the correct pod.
func (c *Container) bindVolumesToPod(podName string) {
	for _, v := range c.volumes {
		v.SetPodName(podName)
	}
}

func (c *Container) Attach(ctx context.Context, processID string, io runtime.ProcessIO) (runtime.Process, error) {
	logger := lagerctx.FromContext(ctx).Session("container-attach", lager.Data{
		"handle":     c.handle,
		"process-id": processID,
	})

	ctx, span := tracing.StartSpan(ctx, "k8s.container.attach", tracing.Attrs{
		"handle":     c.handle,
		"process-id": processID,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()
	// Check if the process has already exited (stored in properties).
	c.mu.RLock()
	statusStr, hasExit := c.properties[exitStatusPropertyName]
	c.mu.RUnlock()
	if hasExit {
		status, err := strconv.Atoi(statusStr)
		if err == nil {
			// A restarted web can preload the persisted exit status into
			// properties before Attach. Returning the bare status there would
			// skip republishing the output locations, and a post-restart read
			// could not reach the producer node's daemon. Agent exec
			// containers therefore take the terminal-evidence path first. If
			// that evidence is unavailable, still return the observed result:
			// Attach must not make attachOrRun execute a completed command
			// again.
			if c.executor == nil || c.metadata.Type != db.ContainerTypeAgent {
				return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
			}
			pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
			if err != nil {
				logger.Error("failed-to-get-pod-for-in-memory-exit", err)
				return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
			}
			if exited, ok := c.exitedProcessWithTerminalEvidence(processID, status, pod); ok {
				c.republishOutputLocations(ctx, logger, pod.Spec.NodeName)
				return exited, nil
			}
			return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
		}
	}

	// Check whether the Pod actually exists in K8s. If it does not, return
	// an error so that attachOrRun falls through to Run() which creates
	// the Pod.
	//
	// A NotFound here is the ordinary first-run path, not a failure: every
	// step Attaches before it Runs, and pods are created lazily in Run.
	// Logging it at error level put one ERROR line in the web log per step,
	// which is exactly the volume that teaches operators to ignore the level
	// and so hides a real attach failure. Any other error (RBAC, apiserver
	// timeout) still deserves the error level and a failed span.
	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Debug("pod-not-created-yet", lager.Data{"pod": c.podName})
			return nil, fmt.Errorf("attach: pod %q not found: %w", c.podName, err)
		}
		logger.Error("failed-to-get-pod", err)
		spanErr = err
		return nil, fmt.Errorf("attach: pod %q not found: %w", c.podName, err)
	}

	// For exec-mode containers (pause pods), in-memory properties are lost
	// on web restart. Check the pod annotation for a persisted exit status.
	if c.executor != nil {
		if statusStr, ok := pod.Annotations[exitStatusAnnotationKey]; ok {
			status, err := strconv.Atoi(statusStr)
			if err == nil {
				// The exit status came from the pod annotation, not memory:
				// this web process never ran uploadOutputsToArtifactStore for
				// this container (web restart, or a fresh Container object).
				// The ArtifactLocator is explicitly ephemeral, so without
				// re-recording here every output volume would wrap with
				// sourceNode=="" and any in-web StreamOut (flight-recorder
				// ingestion, load_var, file-config reads) would fail instantly
				// even though the producer node's daemon still has the data.
				// The pod is still around and knows its node — re-publish the
				// output locations (locator entry + daemon alias, idempotent)
				// before handing back the exited result.
				c.republishOutputLocations(ctx, logger, pod.Spec.NodeName)
				return c.newExitedProcess(processID, status, pod), nil
			}
		}
		// Exec hasn't completed yet (no annotation). Return an error so
		// the engine falls through to Run(), which detects the existing
		// pod and re-execs the command.
		spanErr = fmt.Errorf("attach: exec-mode pod %q has no completion status", c.podName)
		return nil, spanErr
	}

	return newProcess(processID, c.podName, c.clientset, c.config, c, io), nil
}

// terminalSupervisorStateDirValid reports whether a pod's recorded supervisor
// state dir has the exact shape supervisorCommand derives for an agent step:
// the task prefix, "agent-", and an 8-character lowercase hex digest. It is
// the evidence that the annotation on the pod was written by the process this
// container would itself have launched, rather than by anything else.
func terminalSupervisorStateDirValid(stateDir string) bool {
	prefix := taskStateDirPrefix + "agent-"
	if !strings.HasPrefix(stateDir, prefix) || strings.TrimSpace(stateDir) != stateDir || strings.ContainsAny(stateDir, "\\\x00") {
		return false
	}
	suffix := strings.TrimPrefix(stateDir, prefix)
	if len(suffix) != 8 {
		return false
	}
	for _, character := range suffix {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

// exitedProcessWithTerminalEvidence returns a persisted-exit process only when
// the pod itself corroborates the status: same pod, matching exit annotation,
// and a supervisor state dir of the exact shape this container would derive.
func (c *Container) exitedProcessWithTerminalEvidence(processID string, status int, pod *corev1.Pod) (*exitedProcess, bool) {
	if pod == nil || pod.Name != c.podName || pod.Annotations[exitStatusAnnotationKey] != strconv.Itoa(status) || !terminalSupervisorStateDirValid(pod.Annotations[supervisorStateAnnotationKey]) {
		return nil, false
	}
	return c.newExitedProcess(processID, status, pod), true
}

func (c *Container) newExitedProcess(processID string, status int, pod *corev1.Pod) *exitedProcess {
	return &exitedProcess{
		id:            processID,
		result:        runtime.ProcessResult{ExitStatus: status},
		container:     c,
		podName:       pod.Name,
		stateDir:      pod.Annotations[supervisorStateAnnotationKey],
		persistedExit: status,
	}
}

// republishOutputLocations re-records this container's output artifact
// locations with the storage backend (in-memory locator entry + daemon
// alias + best-effort mirror trigger). It is the attach-path counterpart
// of execProcess.uploadOutputsToArtifactStore: when Attach short-circuits
// to an exitedProcess from a pod annotation, the normal upload path never
// runs in this web process, and the ephemeral locator would otherwise
// have no entry for the outputs. Recording is idempotent — volume handles
// and daemon keys are deterministic per (handle, spec) — so re-running it
// for a container whose locations were already recorded is a no-op update.
func (c *Container) republishOutputLocations(ctx context.Context, logger lager.Logger, nodeName string) {
	if c.storageBackend == nil || len(c.volumes) == 0 {
		return
	}
	logger.Info("republishing-output-locations", lager.Data{
		"node":    nodeName,
		"volumes": len(c.volumes),
	})
	c.storageBackend.RecordOutputs(ctx, c.handle, nodeName, c.volumes, c.containerSpec)
}

func (c *Container) Properties() (map[string]string, error) {
	// On first call, load any persisted annotations from the pod. This
	// recovers properties (like resource-result cache) after a web restart.
	if c.podName != "" && c.clientset != nil {
		c.loadAnnotations.Do(func() {
			c.loadPersistedAnnotations()
		})
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	copy := make(map[string]string, len(c.properties))
	for k, v := range c.properties {
		copy[k] = v
	}
	return copy, nil
}

func (c *Container) SetProperty(name string, value string) error {
	c.mu.Lock()
	c.properties[name] = value
	c.mu.Unlock()

	// Persist known properties as pod annotations for crash recovery.
	if c.podName != "" && c.clientset != nil {
		if annotationKey, ok := persistableAnnotations[name]; ok {
			c.annotatePod(annotationKey, value)
		}
	}
	return nil
}

// loadPersistedAnnotations fetches the pod and loads any persisted properties
// from annotations into the in-memory map. Only properties not already in
// the map are loaded (in-memory values take precedence).
func (c *Container) loadPersistedAnnotations() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for propKey, annKey := range persistableAnnotations {
		if value, ok := pod.Annotations[annKey]; ok {
			if _, exists := c.properties[propKey]; !exists {
				c.properties[propKey] = value
			}
		}
	}
}

// annotatePod persists a value as a pod annotation. This is best-effort;
// failures are non-fatal since the property is still in memory.
func (c *Container) annotatePod(annotationKey, value string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	valueJSON, err := json.Marshal(value)
	if err != nil {
		return
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":%s}}}`, annotationKey, string(valueJSON))
	_, _ = c.clientset.CoreV1().Pods(c.config.Namespace).Patch(
		ctx, c.podName, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
}

func (c *Container) DBContainer() db.CreatedContainer {
	return c.dbContainer
}

func (c *Container) createPod(ctx context.Context, processSpec runtime.ProcessSpec) (*corev1.Pod, error) {
	if err := c.allocatePrivateMountSecretNames(processSpec.Dir); err != nil {
		return nil, err
	}
	pod, err := c.buildPod(processSpec, []string{processSpec.Path}, processSpec.Args)
	if err != nil {
		return nil, err
	}
	secrets, err := c.createPrivateMountSecrets(ctx)
	if err != nil {
		return nil, err
	}
	created, err := c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		c.cleanupPrivateMountSecretsAfterCreateError(pod, secrets)
		return nil, err
	}
	if err := c.bindPrivateMountSecrets(ctx, created, secrets); err != nil {
		cleanupErr := c.deletePrivateMountPodAfterSecretFailure(ctx, created, secrets)
		if cleanupErr != nil {
			return nil, fmt.Errorf("bind private task mount: %w (pod cleanup: %v)", err, cleanupErr)
		}
		return nil, err
	}
	return created, nil
}

// pauseCommand is the shell command used by pause pods. It idles forever
// (a looped bounded sleep — busybox sh has no infinite sleep) and exits
// cleanly on SIGTERM so the pod can be stopped. The loop matters: a bare
// `sleep 86400` kills the pod 24h after creation, which silently ends any
// pause that outlives a day.
const pauseCommand = "trap 'exit 0' TERM; while :; do sleep 86400 & wait $!; done"

// createPausePod creates a Pod that runs indefinitely (pause mode) so that
// Process.Wait can exec the real command via the PodExecutor with full
// stdin/stdout/stderr support.
func (c *Container) createPausePod(ctx context.Context, processSpec runtime.ProcessSpec) (*corev1.Pod, error) {
	if err := c.allocatePrivateMountSecretNames(processSpec.Dir); err != nil {
		return nil, err
	}
	pod, err := c.buildPod(processSpec, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		return nil, err
	}
	secrets, err := c.createPrivateMountSecrets(ctx)
	if err != nil {
		return nil, err
	}
	created, err := c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		c.cleanupPrivateMountSecretsAfterCreateError(pod, secrets)
		return nil, err
	}
	if err := c.bindPrivateMountSecrets(ctx, created, secrets); err != nil {
		cleanupErr := c.deletePrivateMountPodAfterSecretFailure(ctx, created, secrets)
		if cleanupErr != nil {
			return nil, fmt.Errorf("bind private task mount: %w (pod cleanup: %v)", err, cleanupErr)
		}
		return nil, err
	}
	return created, nil
}

// buildPod constructs a Pod spec with the given command and args. All other
// fields (image, env, volumes, security, etc.) are derived from the
// Container's spec and config.
func (c *Container) buildPod(processSpec runtime.ProcessSpec, command []string, args []string) (*corev1.Pod, error) {
	if err := c.ensurePrivateMountSecretNames(processSpec.Dir); err != nil {
		return nil, err
	}
	image := resolveImage(c.containerSpec.ImageSpec, c.config.ResourceTypeImages)
	if image == "" {
		typeName := c.containerSpec.ImageSpec.ResourceType
		if typeName == "" {
			typeName = "(unknown)"
		}
		return nil, fmt.Errorf(
			"empty image for resource type %q: configure --resource-type-image %s=<image>",
			typeName, typeName,
		)
	}

	dir := processSpec.Dir
	if dir == "" {
		dir = c.containerSpec.Dir
	}

	env := envVars(c.containerSpec.Env)
	env = append(env, envVars(processSpec.Env)...)
	env = applySecretRefs(env, c.containerSpec.SecretEnv)

	volumes, volumeMounts := c.buildVolumeMounts()
	resources := buildResourceRequirements(c.containerSpec.Limits)
	privileged := c.containerSpec.ImageSpec.Privileged
	if c.containerSpec.Hermetic && privileged {
		return nil, fmt.Errorf("hermetic containers cannot be privileged")
	}

	// Add artifact store PVC volume if configured.
	if artifactVol := c.buildArtifactStoreVolume(); artifactVol != nil {
		volumes = append(volumes, *artifactVol)
	}

	var initContainers []corev1.Container

	// If this container handle was reused (crash recovery), prepend a cleanup
	// init container to remove stale hostPath data before anything else runs.
	if cleanup := c.buildCleanupInitContainer(); cleanup != nil {
		initContainers = append(initContainers, *cleanup)
	}

	artifactInits, err := c.buildArtifactInitContainers(volumes, c.nonPrivateInputMounts(volumeMounts))
	if err != nil {
		return nil, fmt.Errorf("build artifact init containers: %w", err)
	}
	initContainers = append(initContainers, artifactInits...)
	if c.containerSpec.Hermetic {
		if c.storageBackend != nil {
			if prepare := c.storageBackend.BuildHermeticWorkspaceInitContainer(c.nonPrivateInputMounts(volumeMounts)); prepare != nil {
				initContainers = append(initContainers, *prepare)
			}
		}
	}

	// SecretMounts land on the MAIN container only (§8.3) — sidecar
	// containers below receive the original volumeMounts slice and can
	// never see the mounted secret.
	mainMounts := volumeMounts
	if len(c.containerSpec.SecretMounts) > 0 {
		mainMounts = append([]corev1.VolumeMount{}, mainMounts...)
		mode := int32(0400)
		if c.containerSpec.Hermetic {
			// Hermetic pods use a server-owned shared supplemental group so
			// digest-pinned non-root images can read main-only secret files.
			mode = 0440
		}
		for i, sm := range c.containerSpec.SecretMounts {
			name := fmt.Sprintf("secret-mount-%d", i)
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName:  sm.SecretName,
						DefaultMode: &mode,
					},
				},
			})
			mainMounts = append(mainMounts, corev1.VolumeMount{
				Name:      name,
				MountPath: sm.MountPath,
				ReadOnly:  true,
			})
		}
	}
	var managedAuthorityMount *corev1.VolumeMount
	var managedBrokerAuthorityMounts []corev1.VolumeMount
	for index, mount := range c.privateFileMounts() {
		name := c.privateMountSecretName(index)
		if name == "" {
			return nil, fmt.Errorf("private task mount has no allocated Secret name")
		}
		mode := int32(0440)
		volumes = append(volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
				SecretName:  name,
				DefaultMode: &mode,
			}},
		})
		if c.managedOutputBuilderAuthorityIndex() == index {
			managedAuthorityMount = &corev1.VolumeMount{Name: name, MountPath: filepath.Join(mount.MountPath, runtime.ManagedOutputBuilderAuthorityFile), SubPath: runtime.ManagedOutputBuilderAuthorityFile, ReadOnly: true}
			continue
		}
		if c.managedAgentBrokerAuthorityIndex() == index {
			for _, file := range []string{
				runtime.ManagedAgentBrokerAuthorityFile,
				runtime.ManagedAgentBrokerBootstrapFile,
				runtime.ManagedAgentBrokerMCPAccessFile,
			} {
				managedBrokerAuthorityMounts = append(managedBrokerAuthorityMounts, corev1.VolumeMount{
					Name: name, MountPath: filepath.Join(mount.MountPath, file), SubPath: file, ReadOnly: true,
				})
			}
			continue
		}
		mainMounts = append(mainMounts, corev1.VolumeMount{Name: name, MountPath: mount.MountPath, ReadOnly: true})
	}

	var managedBrokerMounts []corev1.VolumeMount
	if broker := c.containerSpec.ManagedAgentBroker; broker != nil {
		if broker.WorkspaceInputPath != "" {
			selected, err := managedAgentBrokerInputMount(broker.WorkspaceInputPath, runtime.ManagedAgentBrokerWorkspaceMountPath, volumeMounts, volumes)
			if err != nil {
				return nil, err
			}
			managedBrokerMounts = append(managedBrokerMounts, selected)
		}
		for _, input := range broker.AttachmentInputs {
			selected, err := managedAgentBrokerInputMount(input.InputPath, input.MountPath, volumeMounts, volumes)
			if err != nil {
				return nil, err
			}
			managedBrokerMounts = append(managedBrokerMounts, selected)
		}
		managedBrokerMounts = append(managedBrokerMounts, managedBrokerAuthorityMounts...)
		scratchQuantity := *resource.NewQuantity(broker.ScratchSizeBytes, resource.BinarySI)
		volumes = append(volumes, corev1.Volume{Name: "agent-broker-scratch", VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &scratchQuantity},
		}})
		managedBrokerMounts = append(managedBrokerMounts, corev1.VolumeMount{
			Name: "agent-broker-scratch", MountPath: runtime.ManagedAgentBrokerScratchMountPath,
		})
		// Hermetic broker pods use the server-owned FSGroup (65534); group
		// read is required for an arbitrary digest-pinned non-root image.
		mode := int32(0440)
		for index, credential := range broker.Credentials {
			name := fmt.Sprintf("agent-broker-credential-%d", index)
			volumes = append(volumes, corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: credential.SecretName, DefaultMode: &mode,
					Items: []corev1.KeyToPath{{Key: credential.Key, Path: "credential"}},
				},
			}})
			managedBrokerMounts = append(managedBrokerMounts, corev1.VolumeMount{
				Name: name, MountPath: credential.MountPath, SubPath: "credential", ReadOnly: true,
			})
		}
	}

	containers := []corev1.Container{
		{
			Name:            mainContainerName,
			Image:           image,
			Command:         command,
			Args:            args,
			WorkingDir:      dir,
			Env:             env,
			VolumeMounts:    mainMounts,
			Resources:       resources,
			SecurityContext: buildContainerSecurityContext(privileged, c.containerSpec.Hermetic),
			ImagePullPolicy: corev1.PullIfNotPresent,
		},
	}

	sidecarContainers, err := buildManagedSidecarContainers(
		c.containerSpec.Sidecars, c.sidecarVolumeMounts(volumeMounts), dir,
		c.containerSpec.SidecarEnv, c.containerSpec.ManagedOutputBuilder, volumes,
		c.containerSpec.ManagedAgentBroker, managedBrokerMounts, c.containerSpec.Hermetic)
	if err != nil {
		return nil, fmt.Errorf("build sidecar containers: %w", err)
	}
	containers = append(containers, sidecarContainers...)
	if managedAuthorityMount != nil {
		for index := range containers {
			if containers[index].Name == runtime.ManagedOutputBuilderName {
				containers[index].VolumeMounts = append(containers[index].VolumeMounts, *managedAuthorityMount)
			}
		}
	}

	// Pause pods trap SIGTERM and exit immediately; 10s is more than
	// enough grace and avoids the default 30s delay during pod teardown.
	var terminationGrace int64 = 10

	affinity := c.buildAffinity()
	var automountServiceAccountToken *bool
	if c.containerSpec.Hermetic || c.containerSpec.ManagedOutputBuilder != nil || c.containerSpec.ManagedAgentBroker != nil {
		disabled := false
		automountServiceAccountToken = &disabled
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        c.podName,
			Namespace:   c.config.Namespace,
			Labels:      c.buildPodLabels(),
			Annotations: c.buildPodAnnotations(),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			SecurityContext:    buildPodSecurityContext(privileged, c.containerSpec.Hermetic),
			ImagePullSecrets:   buildImagePullSecrets(c.config.ImagePullSecrets, c.config.ImageRegistry),
			ServiceAccountName: c.config.ServiceAccount,
			// Hermetic workloads do not need Kubernetes API credentials.
			// Leaving this nil for ordinary pods preserves the namespace's
			// configured service-account automount behavior.
			AutomountServiceAccountToken: automountServiceAccountToken,
			InitContainers:               initContainers,
			Volumes:                      volumes,
			Containers:                   containers,
			Affinity:                     affinity,

			TerminationGracePeriodSeconds: &terminationGrace,
		},
	}, nil
}

// buildArtifactStoreVolume returns a volume for the artifact store via the
// storage backend, or nil if no backend is configured.
func (c *Container) buildArtifactStoreVolume() *corev1.Volume {
	if c.storageBackend == nil {
		return nil
	}
	return c.storageBackend.ArtifactStoreVolume(c.containerSpec.Type)
}

const checkpointSessionVolumeName = "checkpoint-session"

const (
	checkpointRestoreGateVolumeName     = "checkpoint-restore-gate"
	checkpointRestoreGateInitName       = "checkpoint-restore-gate"
	checkpointRestoreGatesDirectoryName = ".checkpoint-restore-gates"
)

const checkpointRestoreGateShell = `
while :; do
  ready=/checkpoint-restore-gate/ready
  if [ -f "$ready" ] && [ ! -L "$ready" ]; then
    receipt="$CHECKPOINT_RESTORE_RECEIPT_SEED:$CHECKPOINT_POD_UID"
    marker="$(cat "$ready")"
    if [ "$(wc -c <"$ready")" -eq "${#receipt}" ] && [ "$marker" = "$receipt" ]; then
      exit 0
    fi
  fi
  sleep 1
done
`

// artifactVolumeName returns the volume name for the artifact store via the
// storage backend, or empty string if no backend is configured.
func (c *Container) artifactVolumeName() string {
	if c.storageBackend == nil {
		return ""
	}
	return c.storageBackend.ArtifactStoreVolumeName()
}

// buildArtifactInitContainers creates init containers for fetching input
// artifacts via the storage backend. Returns nil when no backend is configured.
func (c *Container) buildArtifactInitContainers(podVolumes []corev1.Volume, mainMounts []corev1.VolumeMount) ([]corev1.Container, error) {
	if c.storageBackend == nil {
		return nil, nil
	}
	inputs := make([]runtime.Input, 0, len(c.containerSpec.Inputs))
	for _, input := range c.containerSpec.Inputs {
		if !input.Private {
			inputs = append(inputs, input)
		}
	}
	return c.storageBackend.BuildFetchInitContainers(c.handle, inputs, podVolumes, mainMounts), nil
}

// nonPrivateInputMounts removes any platform-private artifact path from
// generic init preparation. Such inputs are main-only by contract; a generic
// root init container must never receive their mount even if a different
// caller still uses the legacy Private Input facility.
func (c *Container) nonPrivateInputMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	private := make(map[string]struct{})
	for _, input := range c.containerSpec.Inputs {
		if input.Private {
			private[filepath.Clean(input.DestinationPath)] = struct{}{}
		}
	}
	filtered := make([]corev1.VolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		if _, found := private[filepath.Clean(mount.MountPath)]; !found {
			filtered = append(filtered, mount)
		}
	}
	return filtered
}

func (c *Container) privateMountSecretName(index int) string {
	if index < 0 || index >= len(c.privateMountSecretNames) {
		return ""
	}
	return c.privateMountSecretNames[index]
}

// privateFileMounts intentionally reuses the Task 6 Secret lifecycle for the
// managed builder authority. The final entry is never mounted into main;
// buildPod projects it only into the fixed managed sidecar.
func (c *Container) privateFileMounts() []runtime.PrivateFileMount {
	mounts := append([]runtime.PrivateFileMount(nil), c.containerSpec.PrivateFileMounts...)
	if builder := c.containerSpec.ManagedOutputBuilder; builder != nil {
		mounts = append(mounts, builder.Authority)
	}
	if broker := c.containerSpec.ManagedAgentBroker; broker != nil {
		mounts = append(mounts, broker.Authority)
	}
	return mounts
}

func (c *Container) managedOutputBuilderAuthorityIndex() int {
	if c.containerSpec.ManagedOutputBuilder == nil {
		return -1
	}
	return len(c.containerSpec.PrivateFileMounts)
}

func (c *Container) managedAgentBrokerAuthorityIndex() int {
	if c.containerSpec.ManagedAgentBroker == nil {
		return -1
	}
	index := len(c.containerSpec.PrivateFileMounts)
	if c.containerSpec.ManagedOutputBuilder != nil {
		index++
	}
	return index
}

// allocatePrivateMountSecretNames starts a new private-mount epoch for a new
// Pod. Names are random rather than handle-derived so a concurrent caller can
// never predict, substitute, or reuse a prior task's authority Secret.
func (c *Container) allocatePrivateMountSecretNames(processDir string) error {
	if err := c.validatePrivateFileMounts(processDir); err != nil {
		return err
	}
	names := make([]string, len(c.privateFileMounts()))
	seen := make(map[string]struct{}, len(names))
	for i := range names {
		name, err := c.nextPrivateMountSecretName()
		if err != nil {
			return fmt.Errorf("allocate private task mount name: %w", err)
		}
		if _, found := seen[name]; found {
			return fmt.Errorf("duplicate private task mount name")
		}
		seen[name] = struct{}{}
		names[i] = name
	}
	c.privateMountSecretNames = names
	return nil
}

func (c *Container) ensurePrivateMountSecretNames(processDir string) error {
	if len(c.privateMountSecretNames) == len(c.privateFileMounts()) {
		return c.validatePrivateFileMounts(processDir)
	}
	return c.allocatePrivateMountSecretNames(processDir)
}

func (c *Container) nextPrivateMountSecretName() (string, error) {
	if c.privateMountNameGenerator != nil {
		return c.privateMountNameGenerator()
	}
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	// The fixed prefix plus lowercase hex stays comfortably below the DNS-1123
	// label limit and starts/ends with an alphanumeric character.
	return "concourse-private-" + hex.EncodeToString(bytes), nil
}

// createPrivateMountSecrets creates the trusted immutable data before any Pod
// can reference its random name. It deliberately has no owner reference: the
// Pod UID does not exist yet. The subsequent bind is a compare-and-swap update
// of exactly these returned objects, never an adoption of an existing Secret.
func (c *Container) createPrivateMountSecrets(ctx context.Context) ([]*corev1.Secret, error) {
	if len(c.privateFileMounts()) == 0 {
		return nil, nil
	}
	immutable := true
	created := make([]*corev1.Secret, 0, len(c.privateFileMounts()))
	for index, mount := range c.privateFileMounts() {
		name := c.privateMountSecretName(index)
		if name == "" {
			c.deleteExactPrivateMountSecrets(ctx, created)
			return nil, fmt.Errorf("private task mount has no allocated Secret name")
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: c.config.Namespace,
				Labels: map[string]string{
					privateMountSecretLabelKey:  c.handle,
					privateMountPodNameLabelKey: c.podName,
				},
				Annotations: map[string]string{privateMountDataDigestKey: privateMountDataDigest(mount.Files)},
			},
			Type:      corev1.SecretTypeOpaque,
			Immutable: &immutable,
			Data:      mount.Files,
		}
		// Create is intentionally the only operation. AlreadyExists is an
		// authority-substitution attempt (or a vanishingly unlikely collision),
		// never a reason to read, update, or adopt someone else's Secret.
		actual, err := c.clientset.CoreV1().Secrets(c.config.Namespace).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			c.deleteExactPrivateMountSecrets(ctx, created)
			return nil, fmt.Errorf("create private task mount %q: %w", name, err)
		}
		if !privateMountSecretTrusted(actual, mount, c.handle, c.podName) {
			c.deleteExactPrivateMountSecrets(ctx, append(created, actual))
			return nil, fmt.Errorf("created private task mount %q did not preserve trusted immutable data", name)
		}
		created = append(created, actual.DeepCopy())
	}
	return created, nil
}

// bindPrivateMountSecrets adds the exact created Pod UID as owner metadata.
// Update carries the Create response's resourceVersion and UID, so a stale or
// replaced object fails closed instead of being silently adopted.
func (c *Container) bindPrivateMountSecrets(ctx context.Context, pod *corev1.Pod, created []*corev1.Secret) error {
	if len(c.privateFileMounts()) == 0 {
		return nil
	}
	if len(created) != len(c.privateFileMounts()) || pod == nil || pod.UID == "" {
		return fmt.Errorf("cannot bind incomplete private task mounts to pod")
	}
	controller := true
	for index, original := range created {
		if original == nil {
			return fmt.Errorf("private task mount %d was not created", index)
		}
		mount := c.privateFileMounts()[index]
		if !privateMountSecretTrusted(original, mount, c.handle, pod.Name) {
			return fmt.Errorf("private task mount %q is not the created trusted Secret", original.Name)
		}
		bound := original.DeepCopy()
		bound.Labels[privateMountPodUIDLabelKey] = string(pod.UID)
		bound.OwnerReferences = []metav1.OwnerReference{{
			APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID, Controller: &controller,
		}}
		actual, err := c.clientset.CoreV1().Secrets(c.config.Namespace).Update(ctx, bound, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("bind private task mount %q: %w", original.Name, err)
		}
		if actual.UID != original.UID || !privateMountSecretOwnedBy(actual, c.handle, pod) || !privateMountSecretTrusted(actual, mount, c.handle, pod.Name) {
			return fmt.Errorf("bound private task mount %q changed identity or trusted data", original.Name)
		}
	}
	return nil
}

// cleanupPrivateMountSecretsAfterCreateError removes precreated Secrets only
// after a fresh lookup proves the attempted Pod was not created. A Create
// transport error may still have committed the Pod, in which case the
// ownerless reaper will handle the Secrets later.
func (c *Container) cleanupPrivateMountSecretsAfterCreateError(pod *corev1.Pod, secrets []*corev1.Secret) {
	if pod == nil || pod.Name == "" {
		return
	}

	cleanupCtx, cancel := context.WithTimeout(context.Background(), privateMountCreateCleanupTTL)
	defer cancel()

	_, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(cleanupCtx, pod.Name, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		return
	}
	c.deleteExactPrivateMountSecrets(cleanupCtx, secrets)
}

// deletePrivateMountPodAfterSecretFailure requests Pod deletion after a bind
// failure. It removes Secrets only once the API confirms the Pod is absent; a
// successful Delete request alone does not prove kubelet stopped using them.
func (c *Container) deletePrivateMountPodAfterSecretFailure(ctx context.Context, pod *corev1.Pod, created []*corev1.Secret) error {
	if err := c.clientset.CoreV1().Pods(c.config.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete pod after private task mount failure: %w", err)
	}
	_, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return fmt.Errorf("confirm pod deletion after private task mount failure: %w", err)
	}
	c.deleteExactPrivateMountSecrets(ctx, created)
	return nil
}

// deleteExactPrivateMountSecrets may only be used before Pod creation fails,
// or after Pod absence has been confirmed. UID preconditions ensure it cannot
// delete a replacement created by another actor.
func (c *Container) deleteExactPrivateMountSecrets(ctx context.Context, secrets []*corev1.Secret) {
	for _, secret := range secrets {
		if secret == nil || secret.Name == "" {
			continue
		}
		options := metav1.DeleteOptions{}
		if secret.UID != "" {
			options.Preconditions = &metav1.Preconditions{UID: &secret.UID}
		}
		_ = c.clientset.CoreV1().Secrets(c.config.Namespace).Delete(ctx, secret.Name, options)
	}
}

func (c *Container) deleteOwnerBoundPrivateMountSecrets(ctx context.Context, pod *corev1.Pod) {
	for index := range c.privateFileMounts() {
		name := c.privateMountSecretName(index)
		secret, err := c.clientset.CoreV1().Secrets(c.config.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil || !privateMountSecretOwnedBy(secret, c.handle, pod) {
			continue
		}
		c.deleteExactPrivateMountSecrets(ctx, []*corev1.Secret{secret})
	}
}

func (c *Container) validatePrivateFileMounts(processDir string) error {
	if err := runtime.ValidateManagedOutputBuilder(c.containerSpec); err != nil {
		return err
	}
	if err := runtime.ValidateManagedAgentBroker(c.containerSpec); err != nil {
		return err
	}
	privateMounts := c.privateFileMounts()
	if len(privateMounts) == 0 {
		return nil
	}
	_, volumeMounts := c.buildVolumeMounts()
	occupied := make([]string, 0, len(volumeMounts)+len(c.containerSpec.SecretMounts)+1)
	for _, mount := range volumeMounts {
		occupied = append(occupied, resolvePrivateMountPath(mount.MountPath, c.containerSpec.Dir))
	}
	for _, mount := range c.containerSpec.SecretMounts {
		occupied = append(occupied, resolvePrivateMountPath(mount.MountPath, c.containerSpec.Dir))
	}
	if processDir != "" {
		occupied = append(occupied, resolvePrivateMountPath(processDir, c.containerSpec.Dir))
	}

	privatePaths := make([]string, len(privateMounts))
	for index, mount := range privateMounts {
		if err := validatePrivateFileMount(mount); err != nil {
			return err
		}
		path := mount.MountPath
		for _, other := range occupied {
			if pathsOverlap(path, other) {
				return fmt.Errorf("private task mount %q overlaps mounted path %q", path, other)
			}
		}
		for prior := 0; prior < index; prior++ {
			if pathsOverlap(path, privatePaths[prior]) {
				return fmt.Errorf("private task mount %q overlaps private mount %q", path, privatePaths[prior])
			}
		}
		privatePaths[index] = path
	}
	return nil
}

func validatePrivateFileMount(mount runtime.PrivateFileMount) error {
	if !filepath.IsAbs(mount.MountPath) || filepath.Clean(mount.MountPath) != mount.MountPath ||
		mount.MountPath == "/" || mount.MountPath == privateMountRoot ||
		!strings.HasPrefix(mount.MountPath, privateMountRoot+"/") || len(mount.Files) == 0 {
		return fmt.Errorf("invalid private task mount %q", mount.MountPath)
	}
	total := 0
	for name, data := range mount.Files {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name ||
			strings.ContainsAny(name, "/\\\x00") || len(name) > 253 || len(data) == 0 {
			return fmt.Errorf("invalid private task mount file %q", name)
		}
		total += len(data)
		if total > maxPrivateMountSecretBytes {
			return fmt.Errorf("private task mount exceeds Kubernetes Secret size limit")
		}
	}
	return nil
}

func resolvePrivateMountPath(path, dir string) string {
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && dir != "" {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

func pathsOverlap(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left, right = filepath.Clean(left), filepath.Clean(right)
	return left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/")
}

func privateMountSecretOwnedBy(secret *corev1.Secret, handle string, pod *corev1.Pod) bool {
	if secret == nil || pod == nil || pod.UID == "" ||
		secret.Labels[privateMountSecretLabelKey] != handle ||
		secret.Labels[privateMountPodUIDLabelKey] != string(pod.UID) || len(secret.OwnerReferences) != 1 {
		return false
	}
	owner := secret.OwnerReferences[0]
	return owner.APIVersion == "v1" && owner.Kind == "Pod" && owner.Name == pod.Name && owner.UID == pod.UID &&
		owner.Controller != nil && *owner.Controller
}

// privateMountSecretTrusted checks the immutable payload and the metadata
// that identifies a freshly-created, pre-Pod authority Secret. It is used both
// before the binding CAS and after it so neither a raced replacement nor a
// metadata-only mutation can become execution authority.
func privateMountSecretTrusted(secret *corev1.Secret, mount runtime.PrivateFileMount, handle, podName string) bool {
	if secret == nil || secret.Name == "" || secret.Immutable == nil || !*secret.Immutable ||
		secret.Type != corev1.SecretTypeOpaque || secret.Labels[privateMountSecretLabelKey] != handle ||
		secret.Labels[privateMountPodNameLabelKey] != podName ||
		secret.Annotations[privateMountDataDigestKey] != privateMountDataDigest(mount.Files) ||
		privateMountDataDigest(secret.Data) != privateMountDataDigest(mount.Files) {
		return false
	}
	return true
}

func privateMountDataDigest(files map[string][]byte) string {
	// encoding/json sorts string map keys. The digest therefore covers both
	// exact filenames and bytes deterministically without relying on Go map
	// iteration order.
	encoded, err := json.Marshal(files)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum[:])
}

// buildCleanupInitContainer creates an init container that removes stale data
// from the hostPath steps directory for this container handle. Delegates to
// the storage backend. Returns nil when no backend is configured.
func (c *Container) buildCleanupInitContainer() *corev1.Container {
	if c.storageBackend == nil {
		return nil
	}
	return c.storageBackend.BuildCleanupInitContainer(c.handle, c.containerSpec.Type, c.reused)
}

// buildSidecarContainers converts SidecarConfig entries into K8s container
// specs. Each sidecar receives the same volume mounts as the main container
// so it can access inputs, outputs, and caches. Sidecars that do not specify
// their own WorkingDir inherit defaultDir from the main container.
//
// sidecarEnv carries per-sidecar env rows keyed by sidecar name
// (ContainerSpec.SidecarEnv). Env ordering is deterministic: YAML-declared
// literals first (declaration order), then sidecarEnv rows (caller order).
// A sidecar NEVER receives a K8s secret reference: the platform credential
// is main-container-only (§8.1/§8.2), and a sidecar name is
// workflow-authored data that must never act as a credential grant. Nil
// maps are no-ops.
func buildSidecarContainers(
	sidecars []atc.SidecarConfig,
	mainMounts []corev1.VolumeMount,
	defaultDir string,
	sidecarEnv map[string][]string,
	hermetic bool,
) []corev1.Container {
	containers, err := buildManagedSidecarContainers(sidecars, mainMounts, defaultDir, sidecarEnv, nil, nil, nil, nil, hermetic)
	if err != nil {
		return nil
	}
	return containers
}

func buildManagedSidecarContainers(
	sidecars []atc.SidecarConfig,
	mainMounts []corev1.VolumeMount,
	defaultDir string,
	sidecarEnv map[string][]string,
	managedBuilder *runtime.ManagedOutputBuilder,
	podVolumes []corev1.Volume,
	managedBroker *runtime.ManagedAgentBroker,
	managedBrokerMounts []corev1.VolumeMount,
	hermetic bool,
) ([]corev1.Container, error) {
	if len(sidecars) == 0 {
		return nil, nil
	}

	var containers []corev1.Container

	for _, sc := range sidecars {
		var mounts []corev1.VolumeMount
		if sc.Name == runtime.ManagedOutputBuilderName && managedBuilder != nil {
			var err error
			mounts, err = managedOutputBuilderMounts(*managedBuilder, mainMounts, podVolumes)
			if err != nil {
				return nil, err
			}
		} else if sc.Name == runtime.ManagedAgentBrokerName && managedBroker != nil {
			mounts = append([]corev1.VolumeMount(nil), managedBrokerMounts...)
		} else if len(mainMounts) > 0 {
			mounts = append([]corev1.VolumeMount{}, mainMounts...)
		}

		workDir := sc.WorkingDir
		if workDir == "" {
			workDir = defaultDir
		}

		c := corev1.Container{
			Name:            sc.Name,
			Image:           stripImagePrefix(sc.Image),
			Command:         sc.Command,
			Args:            sc.Args,
			WorkingDir:      workDir,
			VolumeMounts:    mounts,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: buildContainerSecurityContext(false, hermetic),
		}

		var env []corev1.EnvVar
		for _, e := range sc.Env {
			env = append(env, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}
		env = append(env, envVars(sidecarEnv[sc.Name])...)
		c.Env = env

		for _, p := range sc.Ports {
			protocol := corev1.ProtocolTCP
			if p.Protocol != "" {
				protocol = corev1.Protocol(p.Protocol)
			}
			c.Ports = append(c.Ports, corev1.ContainerPort{
				ContainerPort: int32(p.ContainerPort),
				Protocol:      protocol,
			})
		}

		if sc.Resources != nil {
			c.Resources = buildSidecarResourceRequirements(*sc.Resources)
		}
		if sc.Name == runtime.ManagedAgentBrokerName && managedBroker != nil {
			c.Resources = buildSidecarResourceRequirements(managedBroker.Resources)
			runAsNonRoot := true
			readOnlyRoot := true
			allowEscalation := false
			c.SecurityContext = &corev1.SecurityContext{
				RunAsNonRoot:             &runAsNonRoot,
				ReadOnlyRootFilesystem:   &readOnlyRoot,
				AllowPrivilegeEscalation: &allowEscalation,
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
				SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			}
			c.ReadinessProbe = &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{
					Command: []string{"/usr/local/bin/agent-broker", "healthcheck"},
				}},
				PeriodSeconds: 2, FailureThreshold: 30,
			}
		}

		containers = append(containers, c)
	}

	return containers, nil
}

func managedAgentBrokerInputMount(inputPath, mountPath string, mounts []corev1.VolumeMount, podVolumes []corev1.Volume) (corev1.VolumeMount, error) {
	selected, err := managedOutputBuilderMounts(runtime.ManagedOutputBuilder{
		InputMountPaths: []string{inputPath},
	}, mounts, podVolumes)
	if err != nil {
		return corev1.VolumeMount{}, fmt.Errorf("managed agent broker workspace: %w", err)
	}
	if len(selected) != 1 {
		return corev1.VolumeMount{}, fmt.Errorf("managed agent broker workspace has no exact mount")
	}
	selected[0].MountPath = mountPath
	selected[0].ReadOnly = true
	return selected[0], nil
}

// managedOutputBuilderMounts selects only the exact typed input/output
// volumes. The normal work-root, flight, cache, scratch, ordinary secret, and
// other sidecar mounts are intentionally absent.
func managedOutputBuilderMounts(builder runtime.ManagedOutputBuilder, mounts []corev1.VolumeMount, podVolumes []corev1.Volume) ([]corev1.VolumeMount, error) {
	selected := make([]corev1.VolumeMount, 0, len(builder.InputMountPaths)+len(builder.OutputMountPaths))
	add := func(path string, readOnly bool) error {
		matches := 0
		for _, mount := range mounts {
			if filepath.Clean(mount.MountPath) != path {
				continue
			}
			if mount.Name == "" {
				return fmt.Errorf("managed output builder projection %q has no runtime volume", path)
			}
			volume, err := managedOutputBuilderPodVolume(mount.Name, podVolumes)
			if err != nil {
				return fmt.Errorf("managed output builder projection %q: %w", path, err)
			}
			for _, other := range mounts {
				if other.Name == mount.Name && filepath.Clean(other.MountPath) != path {
					return fmt.Errorf("managed output builder projection %q shares runtime volume %q with %q", path, mount.Name, other.MountPath)
				}
			}
			for _, other := range podVolumes {
				if other.Name != volume.Name && managedOutputBuilderVolumeSourcesAlias(volume, other) {
					return fmt.Errorf("managed output builder projection %q aliases pod volume %q", path, other.Name)
				}
			}
			matches++
			projection := mount
			projection.ReadOnly = readOnly
			selected = append(selected, projection)
		}
		if matches != 1 {
			return fmt.Errorf("managed output builder projection %q has %d runtime mounts", path, matches)
		}
		return nil
	}
	for _, path := range builder.InputMountPaths {
		if err := add(path, true); err != nil {
			return nil, err
		}
	}
	for _, path := range builder.OutputMountPaths {
		if err := add(path, false); err != nil {
			return nil, err
		}
	}
	return selected, nil
}

func managedOutputBuilderPodVolume(name string, volumes []corev1.Volume) (corev1.Volume, error) {
	var match *corev1.Volume
	for index := range volumes {
		if volumes[index].Name != name {
			continue
		}
		if match != nil {
			return corev1.Volume{}, fmt.Errorf("runtime volume %q is duplicated", name)
		}
		match = &volumes[index]
	}
	if match == nil {
		return corev1.Volume{}, fmt.Errorf("runtime volume %q is missing", name)
	}
	return *match, nil
}

func managedOutputBuilderVolumeSourcesAlias(left, right corev1.Volume) bool {
	if left.HostPath != nil && right.HostPath != nil {
		return filepath.Clean(left.HostPath.Path) == filepath.Clean(right.HostPath.Path)
	}
	return left.PersistentVolumeClaim != nil && right.PersistentVolumeClaim != nil &&
		left.PersistentVolumeClaim.ClaimName == right.PersistentVolumeClaim.ClaimName
}

// buildSidecarResourceRequirements converts SidecarResources to K8s
// ResourceRequirements using Kubernetes quantity strings.
func buildSidecarResourceRequirements(res atc.SidecarResources) corev1.ResourceRequirements {
	reqs := corev1.ResourceRequirements{}

	if res.Requests.CPU != "" || res.Requests.Memory != "" {
		reqs.Requests = corev1.ResourceList{}
		if res.Requests.CPU != "" {
			reqs.Requests[corev1.ResourceCPU] = resource.MustParse(res.Requests.CPU)
		}
		if res.Requests.Memory != "" {
			reqs.Requests[corev1.ResourceMemory] = resource.MustParse(res.Requests.Memory)
		}
	}

	if res.Limits.CPU != "" || res.Limits.Memory != "" {
		reqs.Limits = corev1.ResourceList{}
		if res.Limits.CPU != "" {
			reqs.Limits[corev1.ResourceCPU] = resource.MustParse(res.Limits.CPU)
		}
		if res.Limits.Memory != "" {
			reqs.Limits[corev1.ResourceMemory] = resource.MustParse(res.Limits.Memory)
		}
	}

	return reqs
}

// volumeNameForMountPath finds the K8s volume name that is mounted at the
// given path by scanning the container's volume mounts.
func volumeNameForMountPath(mounts []corev1.VolumeMount, mountPath string) string {
	for _, m := range mounts {
		if m.MountPath == mountPath {
			return m.Name
		}
	}
	return ""
}

// hostPathForVolume returns the hostPath.Path for a named volume, or ""
// if the volume is not hostPath-backed or not found.
func hostPathForVolume(volumes []corev1.Volume, name string) string {
	for _, v := range volumes {
		if v.Name == name && v.HostPath != nil {
			return v.HostPath.Path
		}
	}
	return ""
}

// buildAffinity constructs pod affinity rules via the storage backend.
// Returns nil when no backend is configured.
func (c *Container) buildAffinity() *corev1.Affinity {
	if c.storageBackend == nil {
		return nil
	}
	return c.storageBackend.BuildAffinity(c.containerSpec.Inputs)
}

// buildPodLabels constructs the label map for the pod, including the
// existing worker/type labels plus rich metadata labels for pipeline,
// job, build, step, and handle. Empty metadata fields are omitted.
// Values are truncated to 63 chars (K8s label value limit).
func (c *Container) buildPodLabels() map[string]string {
	labels := map[string]string{
		workerLabelKey: c.workerName,
		typeLabelKey:   string(c.containerSpec.Type),
	}
	if c.containerSpec.Hermetic {
		labels[hermeticLabelKey] = "true"
	}
	if c.containerSpec.ManagedAgentBroker != nil {
		labels["concourse.ci/agent-broker"] = "true"
	}

	addLabel := func(key, value string) {
		if value != "" {
			if len(value) > 63 {
				value = value[:63]
			}
			labels[key] = value
		}
	}

	addLabel("concourse.ci/pipeline", c.metadata.PipelineName)
	addLabel("concourse.ci/job", c.metadata.JobName)
	addLabel("concourse.ci/build", c.metadata.BuildName)
	addLabel("concourse.ci/step", c.metadata.StepName)
	if c.metadata.BuildID != 0 {
		addLabel(buildIDLabelKey, strconv.Itoa(c.metadata.BuildID))
	}
	addLabel(handleLabelKey, c.handle)

	return labels
}

// buildPodAnnotations returns annotations for the pod.
func (c *Container) buildPodAnnotations() map[string]string {
	return map[string]string{}
}

// resolveImage extracts a Kubernetes-compatible image reference from the
// ContainerSpec's ImageSpec. Concourse uses prefixed URLs (docker:///, raw:///)
// while Kubernetes needs bare image references.
//
// For base resource types (time, git, registry-image, etc.), the ImageSpec
// only contains the type name (e.g. "time"). The resourceTypeImages mapping
// translates these to Docker image references (e.g. "concourse/time-resource").
func resolveImage(spec runtime.ImageSpec, resourceTypeImages map[string]string) string {
	image := stripImagePrefix(spec.ImageURL)

	if image == "" {
		image = spec.ResourceType
	}

	// Map base resource type names to their Docker images.
	// Fall back to DefaultResourceTypeImages if no custom mapping is provided.
	mapping := resourceTypeImages
	if mapping == nil {
		mapping = DefaultResourceTypeImages
	}
	if mapped, ok := mapping[image]; ok {
		image = mapped
	}

	return image
}

// stripImagePrefix removes Concourse-internal URL prefixes (docker:///,
// docker://, raw:///) from an image reference so it can be used directly
// as a Kubernetes container image.
func stripImagePrefix(image string) string {
	for _, prefix := range []string{"docker:///", "docker://", "raw:///"} {
		if strings.HasPrefix(image, prefix) {
			return strings.TrimPrefix(image, prefix)
		}
	}
	return image
}

// applySecretRefs converts literal Value entries to ValueFrom.SecretKeyRef
// for env vars with a matching entry in secretEnv, and APPENDS a
// secretKeyRef-only EnvVar (in sorted name order, for deterministic pod
// specs) for secretEnv keys that have no literal entry. This keeps secret
// values out of the pod spec either way. Returns the (possibly grown) slice.
func applySecretRefs(envList []corev1.EnvVar, secretEnv map[string]vars.SecretRef) []corev1.EnvVar {
	if len(secretEnv) == 0 {
		return envList
	}
	refFor := func(ref vars.SecretRef) *corev1.EnvVarSource {
		selector := &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
			Key:                  ref.Key,
		}
		if ref.Optional {
			// kubelet refuses to start a container whose secretKeyRef names a
			// missing key; an optional ref leaves the variable unset instead.
			optional := true
			selector.Optional = &optional
		}
		return &corev1.EnvVarSource{SecretKeyRef: selector}
	}
	seen := map[string]bool{}
	for i := range envList {
		ref, ok := secretEnv[envList[i].Name]
		if !ok {
			continue
		}
		seen[envList[i].Name] = true
		envList[i].Value = ""
		envList[i].ValueFrom = refFor(ref)
	}
	var missing []string
	for name := range secretEnv {
		if !seen[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		envList = append(envList, corev1.EnvVar{Name: name, ValueFrom: refFor(secretEnv[name])})
	}
	return envList
}

func envVars(env []string) []corev1.EnvVar {
	var result []corev1.EnvVar
	for _, e := range env {
		parts := splitEnvVar(e)
		if len(parts) == 2 {
			result = append(result, corev1.EnvVar{
				Name:  parts[0],
				Value: parts[1],
			})
		}
	}
	return result
}

func splitEnvVar(env string) []string {
	for i := 0; i < len(env); i++ {
		if env[i] == '=' {
			return []string{env[:i], env[i+1:]}
		}
	}
	return []string{env}
}

// buildResourceRequirements translates ContainerLimits into K8s resource
// requirements. CPU is mapped to millicores, Memory to bytes.
//
// QoS behavior:
//   - Guaranteed: limits set, no independent requests → requests = limits
//   - Burstable:  both limits and requests set → each mapped independently
//   - Burstable (no cap): only requests set → requests only, no limits
//   - BestEffort: neither set → empty ResourceRequirements
func buildResourceRequirements(limits runtime.ContainerLimits) corev1.ResourceRequirements {
	reqs := corev1.ResourceRequirements{}

	hasLimits := limits.CPU != nil || limits.Memory != nil || limits.EphemeralStorage != nil
	hasRequests := limits.CPURequest != nil || limits.MemoryRequest != nil || limits.EphemeralStorageRequest != nil

	if !hasLimits && !hasRequests {
		return reqs
	}

	if hasLimits {
		res := corev1.ResourceList{}
		if limits.CPU != nil {
			res[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(*limits.CPU), resource.DecimalSI)
		}
		if limits.Memory != nil {
			res[corev1.ResourceMemory] = *resource.NewQuantity(int64(*limits.Memory), resource.BinarySI)
		}
		if limits.EphemeralStorage != nil {
			res[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(*limits.EphemeralStorage), resource.BinarySI)
		}
		reqs.Limits = res

		if !hasRequests {
			// No independent requests → Guaranteed QoS (requests = limits)
			reqs.Requests = res
			return reqs
		}
	}

	// Independent requests specified → Burstable QoS
	reqRes := corev1.ResourceList{}
	if limits.CPURequest != nil {
		reqRes[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(*limits.CPURequest), resource.DecimalSI)
	}
	if limits.MemoryRequest != nil {
		reqRes[corev1.ResourceMemory] = *resource.NewQuantity(int64(*limits.MemoryRequest), resource.BinarySI)
	}
	if limits.EphemeralStorageRequest != nil {
		reqRes[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(*limits.EphemeralStorageRequest), resource.BinarySI)
	}
	reqs.Requests = reqRes

	return reqs
}

// buildPodSecurityContext returns the pod-level security context.
// We intentionally do NOT set RunAsNonRoot here because Concourse resource
// type images (time, git, registry-image, etc.) run as root, and we cannot
// know at pod-creation time whether an arbitrary image supports non-root.
// Container-level AllowPrivilegeEscalation=false still provides hardening.
func buildPodSecurityContext(privileged, hermetic bool) *corev1.PodSecurityContext {
	security := &corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
	if hermetic {
		sharedGroup := int64(65534)
		changePolicy := corev1.FSGroupChangeOnRootMismatch
		security.FSGroup = &sharedGroup
		security.FSGroupChangePolicy = &changePolicy
	}
	return security
}

// buildContainerSecurityContext returns the container-level security context.
// Non-privileged containers disallow privilege escalation.
// Privileged containers get full privileges.
func buildContainerSecurityContext(privileged, hermetic bool) *corev1.SecurityContext {
	if privileged {
		return &corev1.SecurityContext{
			Privileged: &privileged,
		}
	}
	allowEscalation := false
	security := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowEscalation,
	}
	if hermetic {
		security.Capabilities = &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}
	}
	return security
}

// buildImagePullSecrets converts a list of secret names into K8s
// LocalObjectReference entries for the pod spec. If an ImageRegistryConfig
// is provided with a SecretName, it is automatically included (deduplicated).
func buildImagePullSecrets(secretNames []string, registry *ImageRegistryConfig) []corev1.LocalObjectReference {
	seen := make(map[string]bool, len(secretNames)+1)
	var refs []corev1.LocalObjectReference
	for _, name := range secretNames {
		if !seen[name] {
			refs = append(refs, corev1.LocalObjectReference{Name: name})
			seen[name] = true
		}
	}
	if registry != nil && registry.SecretName != "" && !seen[registry.SecretName] {
		refs = append(refs, corev1.LocalObjectReference{Name: registry.SecretName})
	}
	return refs
}

// stableCacheKey returns a deterministic, filesystem-safe key for a task cache
// scoped to a specific job and step. The same job+step+path always produces
// the same key, enabling cache reuse across builds.
func stableCacheKey(jobID int, stepName string, cachePath string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%d\x00%s\x00%s", jobID, stepName, cachePath)
	hash := hex.EncodeToString(h.Sum(nil))[:12]
	// Sanitize stepName for filesystem safety (replace non-alphanumeric with -)
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, stepName)
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("job-%d-%s-%s", jobID, safe, hash)
}

// buildVolumeMounts creates K8s Volume and VolumeMount entries for
// the container's Dir, inputs, outputs, and caches.
// stepVolume creates a volume for a step. When a storage backend is set,
// it delegates to the backend. Otherwise emptyDir.
// Check containers always use emptyDir — their working directory is
// ephemeral and must not persist across check runs via hostPath, since
// the same container handle is reused for every check of the same resource.
func (c *Container) stepVolume(name, subdir string) corev1.Volume {
	if c.storageBackend != nil && c.metadata.Type != db.ContainerTypeCheck {
		return c.storageBackend.StepVolume(name, c.handle, subdir)
	}
	return emptyDirVolume(name)
}

func (c *Container) buildVolumeMounts() ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	idx := 0

	if c.containerSpec.Dir != "" {
		name := fmt.Sprintf("dir-%d", idx)
		idx++
		volumes = append(volumes, c.stepVolume(name, "dir"))
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: c.containerSpec.Dir,
		})
	}

	// Track input mount paths so overlapping outputs can share the same volume.
	// This handles the common Concourse pattern of same-name input+output,
	// where the task reads input data and writes modifications as output.
	inputMountPaths := make(map[string]bool, len(c.containerSpec.Inputs))

	// Build a map of cleaned output path → output name so that when an input
	// overlaps an output, we use the output name as the hostPath subdir.
	// This ensures the daemon filesystem layout matches the daemonKey recorded
	// by recordOutputLocations (which uses the output name).
	outputNameByPath := make(map[string]string)
	for name, path := range c.containerSpec.Outputs {
		outputNameByPath[filepath.Clean(path)] = name
	}

	for _, input := range c.containerSpec.Inputs {
		name := fmt.Sprintf("input-%d", idx)
		idx++
		// When this input path overlaps an output, use the output name as
		// the hostPath subdir so the daemon key matches the filesystem layout.
		subdir := name
		if outName, overlaps := outputNameByPath[filepath.Clean(input.DestinationPath)]; overlaps {
			subdir = outName
		}
		volumes = append(volumes, c.stepVolume(name, subdir))
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: input.DestinationPath,
			ReadOnly:  input.ReadOnly,
		})
		inputMountPaths[filepath.Clean(input.DestinationPath)] = true
	}

	// Sort output names for deterministic ordering.
	outputNames := make([]string, 0, len(c.containerSpec.Outputs))
	for name := range c.containerSpec.Outputs {
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)

	for _, outputName := range outputNames {
		path := c.containerSpec.Outputs[outputName]

		// When an output targets the same path as an input, reuse the
		// input's volume instead of creating a second mount that would
		// shadow it. The trailing slash on output paths is stripped for
		// comparison via filepath.Clean.
		if inputMountPaths[filepath.Clean(path)] {
			continue
		}

		name := fmt.Sprintf("output-%d", idx)
		idx++
		volumes = append(volumes, c.stepVolume(name, outputName))
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: path,
		})
	}

	// Resolve relative cache paths to absolute using the container's working
	// directory. Kubernetes requires absolute paths for volume MountPath.
	resolvedCaches := make([]string, len(c.containerSpec.Caches))
	for i, cachePath := range c.containerSpec.Caches {
		if !filepath.IsAbs(cachePath) && c.containerSpec.Dir != "" {
			cachePath = filepath.Join(c.containerSpec.Dir, cachePath)
		}
		resolvedCaches[i] = cachePath
	}

	// Resolve cache store mode. When CacheStore is set explicitly, use it.
	// Otherwise auto-detect from config fields.
	cacheMode := c.config.CacheStore
	if cacheMode == "" {
		switch {
		case c.storageBackend != nil && len(resolvedCaches) > 0:
			cacheMode = CacheStoreHostPath
		case c.config.CacheHostPath != "" && c.metadata.JobID != 0:
			cacheMode = CacheStoreHostPath
		default:
			cacheMode = CacheStoreEmptyDir
		}
	}

	switch cacheMode {
	case CacheStoreHostPath:
		if c.storageBackend != nil {
			for _, cachePath := range resolvedCaches {
				name := fmt.Sprintf("cache-%d", idx)
				idx++
				volumes = append(volumes, c.storageBackend.CacheVolume(name, c.metadata.JobID, c.metadata.StepName, cachePath))
				mounts = append(mounts, corev1.VolumeMount{
					Name:      name,
					MountPath: cachePath,
				})
			}
		} else {
			// Standalone CacheHostPath mode (no DaemonSet backend).
			basePath := c.config.CacheHostPath
			dirType := corev1.HostPathDirectoryOrCreate
			for _, cachePath := range resolvedCaches {
				key := stableCacheKey(c.metadata.JobID, c.metadata.StepName, cachePath)
				name := fmt.Sprintf("cache-%d", idx)
				idx++
				volumes = append(volumes, corev1.Volume{
					Name: name,
					VolumeSource: corev1.VolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: filepath.Join(basePath, key),
							Type: &dirType,
						},
					},
				})
				mounts = append(mounts, corev1.VolumeMount{
					Name:      name,
					MountPath: cachePath,
				})
			}
		}

	default: // CacheStoreEmptyDir or unknown
		// Ephemeral emptyDir volumes. Caches are lost on pod termination.
		for _, cachePath := range resolvedCaches {
			name := fmt.Sprintf("cache-%d", idx)
			idx++
			volumes = append(volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					EmptyDir: &corev1.EmptyDirVolumeSource{},
				},
			})
			mounts = append(mounts, corev1.VolumeMount{
				Name:      name,
				MountPath: cachePath,
			})
		}
	}

	// Scratch paths — plain emptyDir volumes with no cache semantics.
	for i, scratchPath := range c.containerSpec.ScratchPaths {
		if !filepath.IsAbs(scratchPath) && c.containerSpec.Dir != "" {
			scratchPath = filepath.Join(c.containerSpec.Dir, scratchPath)
		}
		name := fmt.Sprintf("scratch-%d", i)
		volumes = append(volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: scratchPath,
		})
	}

	return volumes, mounts
}

// sidecarVolumeMounts preserves the legacy shared task filesystem while
// withholding only platform-created private input mounts from sidecars.
func (c *Container) sidecarVolumeMounts(mounts []corev1.VolumeMount) []corev1.VolumeMount {
	private := make(map[string]struct{})
	for _, input := range c.containerSpec.Inputs {
		if input.Private {
			private[filepath.Clean(input.DestinationPath)] = struct{}{}
		}
	}
	filtered := make([]corev1.VolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		if _, found := private[filepath.Clean(mount.MountPath)]; !found {
			filtered = append(filtered, mount)
		}
	}
	return filtered
}
