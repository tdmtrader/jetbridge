package jetbridge

import (
	"context"
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
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/tracing"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	exitStatusPropertyName      = "concourse:exit-status"
	resourceResultPropertyName  = "concourse:resource-result"
	mainContainerName           = "main"
	exitStatusAnnotationKey     = "concourse.ci/exit-status"
	buildIDLabelKey             = "concourse.ci/build-id"
	resourceResultAnnotationKey = "concourse.ci/resource-result"
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
	recordWitness   func(context.Context, executioncontrol.Acknowledgement) error
	checkStart      func(context.Context) error
	handle          string
	podName         string
	metadata        db.ContainerMetadata
	containerSpec   runtime.ContainerSpec
	dbContainer     db.CreatedContainer
	clientset       kubernetes.Interface
	config          Config
	workerName      string
	mu              sync.RWMutex
	properties      map[string]string
	loadAnnotations sync.Once
	executor        PodExecutor
	volumes         []*Volume
	storageBackend  StorageBackend
	// lookedUp is true when the Container was built by LookupContainer
	// (fly hijack/intercept). Such a Container has no ContainerSpec -- it can
	// only attach to a pod some step already created, never build one.
	lookedUp bool
	// reused is true when FindOrCreateContainer found an existing container
	// in the DB (crash-recovery path). In DaemonSet mode this means the
	// hostPath directory may contain stale data and needs cleanup.
	reused bool

	// outputControls reaches the output daemon on whichever node this
	// container's Pod lands on. Nil on every deployment with no output plane,
	// and nil is the ordinary path rather than a degraded one.
	outputControls OutputControlResolver

	// captureClass reads what the output ledger says about this container's
	// step directory, for the two operations that cannot take a writer ticket:
	// hijacking a looked-up container and replacing a terminal pause Pod.
	captureClass captureClassifier
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
	storageBackend StorageBackend,
	reused bool,
	lookedUp bool,
) *Container {
	// The ledger classifier, wired HERE and nowhere else.
	//
	// It was never assigned in production before this pass, which made
	// refuseIfCaptureHeld a no-op on every path: pause pod recreation and
	// hijack over a capture-held source were refused only in tests that
	// supplied the collaborator themselves. It is nil when there is no output
	// plane and when there is no storage backend, and both nils are correct:
	// the guard fails CLOSED on an unreadable ledger, so a worker with no
	// daemon to ask must not be given something to ask.
	var classifier captureClassifier
	if config.OutputPlaneEnabled && storageBackend != nil {
		if daemonSet, ok := storageBackend.(*DaemonSetBackend); ok {
			classifier = daemonSet
		}
	}

	return &Container{
		handle:         handle,
		captureClass:   classifier,
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
		lookedUp:       lookedUp,
		reused:         reused,
	}
}

func (c *Container) Run(ctx context.Context, spec runtime.ProcessSpec, io runtime.ProcessIO) (runtime.Process, error) {
	if c.checkStart != nil {
		if err := c.checkStart(ctx); err != nil {
			return nil, err
		}
	}
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
			// A hijack is a new writer over the step's tree, and the ATC has
			// no execution identity for a looked-up container -- so it cannot
			// take a ticket on that writer's behalf. Req 18 says a
			// capture-enabled task loses post-completion hijack; this is
			// where that loss is spelled, as a refusal that names itself
			// rather than a session that races the capture.
			if err := c.refuseIfCaptureHeld(ctx, "intercepting this container"); err != nil {
				return nil, err
			}
		}

		if getErr == nil && (existingPod.Status.Phase == corev1.PodSucceeded || existingPod.Status.Phase == corev1.PodFailed) {
			// A replacement is a NEW POD UID getting a write-capable mount over
			// this step's tree, and for a capture-selected step that tree is
			// the reserved incarnation. Req 16 forbids one over a held source,
			// and this path has no execution identity to take a writer ticket
			// with, so it asks the ledger instead.
			//
			// This is the OTHER pause-pod replacement site. execProcess's
			// recreatePausePod covers a pod that died after Run returned; this
			// one covers a pod that was already terminal when Run was called,
			// which is the same damage reached by a different route. The
			// ordinary path is unchanged: with nothing held the classifier
			// answers unmanaged and the pod is replaced exactly as before.
			if err := c.refuseIfCaptureHeld(ctx, "replacing this step's terminal pause pod"); err != nil {
				return nil, err
			}

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

	if c.lookedUp {
		return nil, fmt.Errorf("container %q cannot be intercepted: worker %q has no exec transport configured", c.handle, c.workerName)
	}

	// Fallback direct mode: only used when no executor is configured
	// (e.g. tests that don't set up SPDY). Bakes command into Pod spec.
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
			return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
		}
	}

	// Check whether the Pod actually exists in K8s. If it does not, return
	// an error so that attachOrRun falls through to Run() which creates
	// the Pod.
	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		logger.Error("failed-to-get-pod", err)
		spanErr = err
		return nil, fmt.Errorf("attach: pod %q not found: %w", c.podName, err)
	}

	// For exec-mode containers (pause pods), in-memory properties are lost
	// on web restart. Check the pod annotation for a persisted exit status.
	if c.executor != nil {
		// No annotation means the exec has not completed. The error makes
		// the engine fall through to Run(), which detects the existing pod
		// and re-execs the command.
		var process runtime.Process
		process, spanErr = recoverExecProcess(c.podName, processID, pod)
		return process, spanErr
	}

	return newProcess(processID, c.podName, c.clientset, c.config, c, io), nil
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
	pod, err := c.buildPod(processSpec, []string{processSpec.Path}, processSpec.Args)
	if err != nil {
		return nil, err
	}
	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// pauseCommand is the shell command used by pause pods. It sleeps
// indefinitely and exits cleanly on SIGTERM so the pod can be stopped.
const pauseCommand = "trap 'exit 0' TERM; sleep 86400 & wait"

// createPausePod creates a Pod that runs indefinitely (pause mode) so that
// Process.Wait can exec the real command via the PodExecutor with full
// stdin/stdout/stderr support.
func (c *Container) createPausePod(ctx context.Context, processSpec runtime.ProcessSpec) (*corev1.Pod, error) {
	pod, err := c.buildPod(processSpec, []string{"sh", "-c", pauseCommand}, nil)
	if err != nil {
		return nil, err
	}
	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// buildPod constructs a Pod spec with the given command and args. All other
// fields (image, env, volumes, security, etc.) are derived from the
// Container's spec and config.
func (c *Container) buildPod(processSpec runtime.ProcessSpec, command []string, args []string) (*corev1.Pod, error) {
	if err := c.validateInputs(); err != nil {
		return nil, err
	}
	// The envelope is validated against the SPEC, before any container is
	// composed: an undeclared output, an output overlapping a strict input or
	// a capture riding the base capability must be a refusal rather than a pod
	// that comes back and then cannot be held.
	if err := c.containerSpec.ExecutionControl.Validate(c.containerSpec); err != nil {
		return nil, err
	}
	// And the FACET, which the envelope cannot carry: a spec may be admitted
	// with a capture by a control plane that believes the plane is on, and land
	// on a worker whose output facet is not enabled.
	//
	// This is a refusal at ADMISSION rather than an omission in the Pod. The
	// difference is the whole of Req 58: a worker that quietly built the
	// ordinary pod would produce a step that ran, succeeded and captured
	// nothing, and the handoff the control plane predeclared would sit
	// unresolved until its deadline. There is no cache-tier fallback to
	// degrade into, so the honest answer is that no capture pod is built.
	if c.containerSpec.ExecutionControl.HasDurableOutputCapture() {
		if !c.config.OutputPlaneEnabled {
			return nil, fmt.Errorf("%w: this step selected durable output capture and this "+
				"worker's output facet is not enabled, so no capture pod is built. Durable "+
				"output capture never degrades into an ordinary step: a pod that ran and "+
				"captured nothing would leave the predeclared handoff unresolved until its "+
				"deadline", runtime.ErrInvalidExecutionControl)
		}
		// And the EPOCH, which is the part a ready label cannot attest.
		//
		// A label says a node's daemon was up and attested at some point; it
		// does not say which cohort it belongs to. A node can carry the label
		// while its daemons speak for a different activation epoch -- a
		// rolling upgrade, a half-finished rotation, a node back from a long
		// drain -- and a capture admitted against that cohort would be signed
		// by a key this control plane does not pin. Req 57: a stale label or
		// handshake authorizes nothing.
		if epoch := c.config.OutputActivationEpoch; epoch != 0 &&
			int64(c.containerSpec.ExecutionControl.ActivationEpoch) != epoch {
			return nil, fmt.Errorf("%w: this step was admitted under activation epoch %d and "+
				"this worker speaks for epoch %d, so no capture pod is built. A ready label "+
				"is a scheduling hint and never authority; the authenticated handshake and "+
				"the activation epoch row are",
				runtime.ErrInvalidExecutionControl,
				c.containerSpec.ExecutionControl.ActivationEpoch, epoch)
		}
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
	applySecretRefs(env, c.containerSpec.SecretEnv)

	volumes, volumeMounts := c.buildVolumeMounts()
	resources := buildResourceRequirements(c.containerSpec.Limits)
	privileged := c.containerSpec.ImageSpec.Privileged

	// Add the artifact storage volume, when a backend is configured.
	if artifactVol := c.buildArtifactStoreVolume(); artifactVol != nil {
		volumes = append(volumes, *artifactVol)
	}

	var initContainers []corev1.Container

	// The capture control init goes FIRST, before every writer.
	//
	// Requirement 3 puts the durable source hold ahead of the producer main
	// process, and every other container this pod builds writes into the tree
	// that hold protects: `cleanup-stale` removes it, `artifact-fetch` stages
	// inputs into it, the main container and its sidecars produce into it. So
	// "before the main process" is not enough; it is index 0 of the init
	// slice, and the ordering scenario asserts the index rather than mere
	// membership.
	//
	// An execution that selected no capture reaches none of this: the ordinary
	// pod is byte-identical to the one this runtime built before the output
	// plane existed, which is Req 59 and the control scenario for the whole
	// capture-pod feature.
	//
	// OutputPlaneEnabled deliberately does NOT gate this, and Phase 8 is where
	// that changes. The flag gates the cleanup probe and the ATC's own control
	// calls -- the places where a worker with no output daemon would otherwise
	// dial one -- but the capture init is emitted whenever the spec carries a
	// capture, because a spec only carries one if the control plane put it
	// there. Phase 8's scenario `A worker whose output facet is not enabled
	// builds no capture pod` needs the SELECTION gated rather than the init,
	// which is a refusal at admission and not an omission here; that is where
	// this flag becomes load-bearing on this path.
	if captureInit := c.buildCaptureControlInitContainer(); captureInit != nil {
		initContainers = append(initContainers, *captureInit)
	}

	// If this container handle was reused (crash recovery), prepend a cleanup
	// init container to remove stale hostPath data before anything else runs.
	cleanup, err := c.buildCleanupInitContainer()
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		initContainers = append(initContainers, *cleanup)
	}

	artifactInits, err := c.buildArtifactInitContainers(volumes, volumeMounts)
	if err != nil {
		return nil, fmt.Errorf("build artifact init containers: %w", err)
	}
	initContainers = append(initContainers, artifactInits...)

	containers := []corev1.Container{
		{
			Name:            mainContainerName,
			Image:           image,
			Command:         command,
			Args:            args,
			WorkingDir:      dir,
			Env:             env,
			VolumeMounts:    volumeMounts,
			Resources:       resources,
			SecurityContext: buildContainerSecurityContext(privileged),
			ImagePullPolicy: corev1.PullIfNotPresent,
		},
	}

	containers = append(containers, buildSidecarContainers(c.containerSpec.Sidecars, volumeMounts, dir)...)

	// Pause pods trap SIGTERM and exit immediately; 10s is more than
	// enough grace and avoids the default 30s delay during pod teardown.
	var terminationGrace int64 = 10

	affinity := c.buildAffinity()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        c.podName,
			Namespace:   c.config.Namespace,
			Labels:      c.buildPodLabels(),
			Annotations: c.buildPodAnnotations(),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			SecurityContext:    buildPodSecurityContext(privileged),
			ImagePullSecrets:   buildImagePullSecrets(c.config.ImagePullSecrets, c.config.ImageRegistry),
			ServiceAccountName: c.config.ServiceAccount,
			InitContainers:     initContainers,
			Volumes:            volumes,
			Containers:         containers,
			Affinity:           affinity,

			TerminationGracePeriodSeconds: &terminationGrace,
		},
	}
	if err := validatePodVolumeMounts(pod); err != nil {
		return nil, err
	}
	return pod, nil
}

func (c *Container) validateInputs() error {
	for _, input := range c.containerSpec.Inputs {
		if input.RunInput != "" && input.HangarRead == nil {
			return fmt.Errorf("Run input has no admitted read lease")
		}
		if input.HangarRead != nil && (input.HangarTree == nil || input.HangarRead.Validate() != nil || input.HangarRead.Ref != *input.HangarTree) {
			return fmt.Errorf("managed input lacks its exact read authority")
		}
		hasArtifact := input.Artifact != nil
		hasHangarTree := input.HangarTree != nil
		if hasArtifact == hasHangarTree {
			return fmt.Errorf("input %q must set exactly one of Artifact or HangarTree", input.DestinationPath)
		}
		if !hasHangarTree {
			continue
		}
		if !c.config.HangarEnabled {
			return fmt.Errorf("Hangar tree input %q was presented while Hangar is disabled", input.DestinationPath)
		}
		if c.storageBackend == nil {
			return fmt.Errorf("Hangar tree input %q requires node-local storage", input.DestinationPath)
		}
		if err := input.HangarTree.Validate(); err != nil {
			return fmt.Errorf("invalid Hangar tree input %q: %w", input.DestinationPath, err)
		}
		for outputName, outputPath := range c.containerSpec.Outputs {
			if containerPathsOverlap(outputPath, input.DestinationPath) {
				return fmt.Errorf("Hangar tree input %q must not overlap output %q", input.DestinationPath, outputName)
			}
		}
	}
	return nil
}

func containerPathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	if parent == child {
		return true
	}
	separator := string(filepath.Separator)
	if parent == separator {
		return filepath.IsAbs(child)
	}
	return strings.HasPrefix(child, parent+separator)
}

func validatePodVolumeMounts(pod *corev1.Pod) error {
	declared := make(map[string]struct{}, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		declared[volume.Name] = struct{}{}
	}
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range containers {
			for _, mount := range container.VolumeMounts {
				if _, found := declared[mount.Name]; !found {
					return fmt.Errorf("container %q volume mount %q has no matching Pod volume", container.Name, mount.Name)
				}
			}
		}
	}
	return nil
}

// buildArtifactStoreVolume returns a volume for the artifact store via the
// storage backend, or nil if no backend is configured.
func (c *Container) buildArtifactStoreVolume() *corev1.Volume {
	if c.storageBackend == nil {
		return nil
	}
	return c.storageBackend.ArtifactStoreVolume(c.containerSpec.Type)
}

// buildArtifactInitContainers creates init containers for fetching input
// artifacts via the storage backend. Returns nil when no backend is configured.
func (c *Container) buildArtifactInitContainers(podVolumes []corev1.Volume, mainMounts []corev1.VolumeMount) ([]corev1.Container, error) {
	if c.storageBackend == nil {
		return nil, nil
	}
	return c.storageBackend.BuildFetchInitContainers(c.handle, c.containerSpec.Inputs, podVolumes, mainMounts)
}

// buildCleanupInitContainer creates an init container that removes stale data
// from the hostPath steps directory for this container handle. Delegates to
// the storage backend. Returns nil when no backend is configured.
func (c *Container) buildCleanupInitContainer() (*corev1.Container, error) {
	if c.storageBackend == nil {
		return nil, nil
	}
	return c.storageBackend.BuildCleanupInitContainer(c.handle, c.containerSpec.Type, c.reused)
}

// buildSidecarContainers converts SidecarConfig entries into K8s container
// specs. Each sidecar receives the same volume mounts as the main container
// so it can access inputs, outputs, and caches. Sidecars that do not specify
// their own WorkingDir inherit defaultDir from the main container.
func buildSidecarContainers(sidecars []atc.SidecarConfig, mainMounts []corev1.VolumeMount, defaultDir string) []corev1.Container {
	if len(sidecars) == 0 {
		return nil
	}

	allowEscalation := false
	var containers []corev1.Container

	for _, sc := range sidecars {
		var mounts []corev1.VolumeMount
		if len(mainMounts) > 0 {
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
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowEscalation,
			},
		}

		for _, e := range sc.Env {
			c.Env = append(c.Env, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}

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

		containers = append(containers, c)
	}

	return containers
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
	var affinity *corev1.Affinity
	if c.storageBackend != nil {
		affinity = c.storageBackend.BuildAffinity(c.containerSpec.Inputs, c.containerSpec.ExecutionControl)
	}
	control := c.containerSpec.ExecutionControl
	if control == nil || control.Node == nil {
		return affinity
	}
	if affinity == nil {
		affinity = &corev1.Affinity{}
	}
	if affinity.NodeAffinity == nil {
		affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	if affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	selector := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if len(selector.NodeSelectorTerms) == 0 {
		selector.NodeSelectorTerms = []corev1.NodeSelectorTerm{{}}
	}
	for i := range selector.NodeSelectorTerms {
		selector.NodeSelectorTerms[i].MatchFields = append(selector.NodeSelectorTerms[i].MatchFields, corev1.NodeSelectorRequirement{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{control.Node.Name}})
	}
	return affinity
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
//
// The reservation is stamped here because a looked-up Container has no
// ContainerSpec to read it from -- `LookupContainer` builds one with an empty
// spec -- and the hijack refusal Req 18 requires has to know WHICH directory
// the capture holds. The handle is a sibling of it, so a guard that fell back
// to the handle could only ever be told `unmanaged`.
//
// The ATC composes nothing: ReservedDirectory came off the wire from
// `reserve-incarnation` and was validated against the incarnation beside it.
func (c *Container) buildPodAnnotations() map[string]string {
	annotations := map[string]string{}
	if reserved := captureReservedDirectory(c.containerSpec); reserved != "" {
		annotations[captureReservationAnnotation] = reserved
	}

	return annotations
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

// applySecretRefs replaces literal Value entries with ValueFrom.SecretKeyRef
// for env vars that have a matching entry in secretEnv. This prevents secret
// values from appearing in the pod spec.
func applySecretRefs(envList []corev1.EnvVar, secretEnv map[string]vars.SecretRef) {
	if len(secretEnv) == 0 {
		return
	}
	for i := range envList {
		ref, ok := secretEnv[envList[i].Name]
		if !ok {
			continue
		}
		envList[i].Value = ""
		envList[i].ValueFrom = &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name},
				Key:                  ref.Key,
			},
		}
	}
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
func buildPodSecurityContext(privileged bool) *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// buildContainerSecurityContext returns the container-level security context.
// Non-privileged containers disallow privilege escalation.
// Privileged containers get full privileges.
func buildContainerSecurityContext(privileged bool) *corev1.SecurityContext {
	if privileged {
		return &corev1.SecurityContext{
			Privileged: &privileged,
		}
	}
	allowEscalation := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowEscalation,
	}
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
func stableCacheKey(identity atc.TaskCacheIdentity, stepName string, cachePath string) string {
	h := sha256.New()
	if identity.JobID > 0 {
		fmt.Fprintf(h, "%d\x00%s\x00%s", identity.JobID, stepName, cachePath)
	} else {
		fmt.Fprintf(h, "run-task-cache/v1\x00%d\x00%d\x00%s\x00%s\x00%s", identity.TeamID, identity.TemplatePipelineID, identity.RunJobName, stepName, cachePath)
	}
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
	if identity.JobID > 0 {
		return fmt.Sprintf("job-%d-%s-%s", identity.JobID, safe, hash)
	}
	return fmt.Sprintf("run-%d-%d-%s-%s", identity.TeamID, identity.TemplatePipelineID, safe, hash)
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

// outputVolume is stepVolume for a declared output, with one exception: the ONE
// output selected for capture mounts the incarnation the output daemon
// reserved, not this step's own directory.
//
// The two used to be the same call and that was the seam Phase 4 found. A
// capture-selected producer wrote into `steps/<handle>/<output>` while the
// daemon's hold protected `steps/<execution>.<generation>/<output>` -- sibling
// directories -- so the capture sealed an empty tree and every path-keyed guard
// correctly answered "unmanaged" about the bytes that mattered.
//
// The ATC chooses nothing here. `ReservedDirectory` came off the wire from
// `reserve-incarnation`, was validated against the incarnation it carries, and
// is repeated. Req 7 holds because the daemon named the path.
func (c *Container) outputVolume(name, outputName string) corev1.Volume {
	reserved := captureReservedDirectory(c.containerSpec)
	if reserved == "" || outputName != captureSelectedOutputName(c.containerSpec) {
		return c.stepVolume(name, outputName)
	}
	if c.storageBackend == nil || c.metadata.Type == db.ContainerTypeCheck {
		// No artifact store, or a check container, whose working directory is
		// deliberately ephemeral. Neither can carry a capture; the envelope
		// would not have validated against a spec with no declared output, and
		// an emptyDir here is the same thing the step's other volumes get.
		return emptyDirVolume(name)
	}

	return c.storageBackend.ReservedIncarnationVolume(name, reserved)
}

// inputVolumeName is shared by Pod construction and managed-read admission.
func inputVolumeName(spec runtime.ContainerSpec, index int) string {
	if spec.Dir != "" {
		index++
	}
	return fmt.Sprintf("input-%d", index)
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

	for i, input := range c.containerSpec.Inputs {
		name := inputVolumeName(c.containerSpec, i)
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
			ReadOnly:  input.HangarTree != nil,
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
		volumes = append(volumes, c.outputVolume(name, outputName))
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
		case c.storageBackend != nil && len(resolvedCaches) > 0 && c.containerSpec.TaskCacheIdentity != nil:
			cacheMode = CacheStoreHostPath
		case c.config.CacheHostPath != "" && c.containerSpec.TaskCacheIdentity != nil:
			cacheMode = CacheStoreHostPath
		default:
			cacheMode = CacheStoreEmptyDir
		}
	}
	if cacheMode == CacheStoreHostPath && c.containerSpec.TaskCacheIdentity == nil {
		cacheMode = CacheStoreEmptyDir
	}

	switch cacheMode {
	case CacheStoreHostPath:
		if c.storageBackend != nil {
			for _, cachePath := range resolvedCaches {
				name := fmt.Sprintf("cache-%d", idx)
				idx++
				volumes = append(volumes, c.storageBackend.CacheVolume(name, *c.containerSpec.TaskCacheIdentity, c.metadata.StepName, cachePath))
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
				key := stableCacheKey(*c.containerSpec.TaskCacheIdentity, c.metadata.StepName, cachePath)
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
