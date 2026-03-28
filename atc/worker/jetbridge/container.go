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
	"github.com/concourse/concourse/tracing"
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
	handle        string
	podName       string
	metadata      db.ContainerMetadata
	containerSpec runtime.ContainerSpec
	dbContainer   db.CreatedContainer
	clientset     kubernetes.Interface
	config        Config
	workerName    string
	mu              sync.RWMutex
	properties      map[string]string
	loadAnnotations sync.Once
	executor        PodExecutor
	volumes         []*Volume
	artifactLocator *ArtifactLocator
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
	artifactLocator *ArtifactLocator,
) *Container {
	return &Container{
		handle:          handle,
		podName:         GeneratePodName(metadata, handle),
		metadata:        metadata,
		containerSpec:   containerSpec,
		dbContainer:     dbContainer,
		clientset:       clientset,
		config:          config,
		workerName:      workerName,
		properties:      make(map[string]string),
		executor:        executor,
		volumes:         volumes,
		artifactLocator: artifactLocator,
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
		"pod_name":  c.handle,
	})
	var err error
	defer func() { tracing.End(span, err) }()

	processID := c.handle
	if spec.ID != "" {
		processID = spec.ID
	}

	// Exec mode: use a pause Pod and exec the real command via SPDY.
	// If the pod already exists (e.g. fly hijack into an existing container),
	// reuse it. Otherwise create a new pause pod.
	if execMode {
		podName := c.podName
		_, err = c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
		if err != nil {
			// Pod doesn't exist — create a new pause pod.
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
		return newExecProcess(processID, podName, c.clientset, c.config, c, c.executor, spec, io), nil
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
func (c *Container) outputPaths() map[string]bool {
	if len(c.containerSpec.Outputs) > 0 {
		paths := make(map[string]bool, len(c.containerSpec.Outputs))
		for _, path := range c.containerSpec.Outputs {
			paths[path] = true
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
		if statusStr, ok := pod.Annotations[exitStatusAnnotationKey]; ok {
			status, err := strconv.Atoi(statusStr)
			if err == nil {
				return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
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

	volumes, volumeMounts := c.buildVolumeMounts()
	resources := buildResourceRequirements(c.containerSpec.Limits)
	privileged := c.containerSpec.ImageSpec.Privileged

	// Add artifact store PVC volume if configured.
	if artifactVol := c.buildArtifactStoreVolume(); artifactVol != nil {
		volumes = append(volumes, *artifactVol)
	}

	initContainers, err := c.buildArtifactInitContainers(volumeMounts)
	if err != nil {
		return nil, fmt.Errorf("build artifact init containers: %w", err)
	}

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

	containers = append(containers, buildSidecarContainers(c.containerSpec.Sidecars, volumeMounts)...)

	// Pause pods trap SIGTERM and exit immediately; 10s is more than
	// enough grace and avoids the default 30s delay during pod teardown.
	var terminationGrace int64 = 10

	affinity := c.buildAffinity()

	return &corev1.Pod{
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
	}, nil
}

// artifactDaemonHostPathVolumeName is the pod volume name for the DaemonSet
// hostPath artifact store.
const artifactDaemonHostPathVolumeName = "artifact-daemon-hostpath"

// buildArtifactStoreVolume returns a hostPath volume for the DaemonSet artifact
// store, or nil if ArtifactDaemonHostPath is not configured.
func (c *Container) buildArtifactStoreVolume() *corev1.Volume {
	if c.metadata.Type == db.ContainerTypeCheck {
		return nil
	}

	if c.config.ArtifactDaemonHostPath == "" {
		return nil
	}

	hostPathType := corev1.HostPathDirectoryOrCreate
	return &corev1.Volume{
		Name: artifactDaemonHostPathVolumeName,
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: c.config.ArtifactDaemonHostPath,
				Type: &hostPathType,
			},
		},
	}
}

// artifactVolumeName returns the volume name for the DaemonSet hostPath
// artifact store.
func (c *Container) artifactVolumeName() string {
	return artifactDaemonHostPathVolumeName
}

// buildArtifactInitContainers creates init containers for each input with a
// non-nil artifact when ArtifactDaemonHostPath is configured. Each init container
// sends a resolve request to the local artifact-daemon's /resolve endpoint,
// which copies the artifact data into the input volume's hostPath directory.
func (c *Container) buildArtifactInitContainers(mainMounts []corev1.VolumeMount) ([]corev1.Container, error) {
	if c.config.ArtifactDaemonHostPath == "" {
		return nil, nil
	}

	helperImage := c.config.ArtifactHelperImage
	if helperImage == "" {
		helperImage = DefaultArtifactHelperImage
	}

	allowEscalation := false

	var inits []corev1.Container
	for i, input := range c.containerSpec.Inputs {
		if input.Artifact == nil {
			continue
		}

		// Find the volume name for this input's destination path from the
		// main container's volume mounts.
		volumeName := volumeNameForMountPath(mainMounts, input.DestinationPath)
		if volumeName == "" {
			continue
		}

		key := ArtifactKey(input.Artifact.Handle())

		// Look up the daemon key from the locator. For fresh get/task outputs,
		// recordOutputLocations stored "<container-handle>/<subdir>" as HostDir.
		// For cached resource volumes (cache hit, no pod ran), the locator may
		// not have an entry — fall back to the volume handle as the daemon key.
		// The daemon's filesystem scan can still find it on disk.
		daemonKey := key // fallback: use volume handle directly
		if loc, hasLoc := c.artifactLocate(key); hasLoc {
			daemonKey = loc.HostDir
		}

		// The daemon key maps to steps/<key> on the daemon's filesystem. The dest is the
		// host path of this init container's input volume.
		hostDestPath := filepath.Join(c.config.ArtifactDaemonHostPath, "steps", c.handle, fmt.Sprintf("input-%d", i))

		initContainer := corev1.Container{
			Name:    fmt.Sprintf("fetch-input-%d", i),
			Image:   helperImage,
			Command: c.daemonResolveCommand(daemonKey, hostDestPath),
			VolumeMounts: []corev1.VolumeMount{
				{Name: c.artifactVolumeName(), MountPath: ArtifactMountPath, ReadOnly: true},
				{Name: volumeName, MountPath: input.DestinationPath},
			},
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowEscalation,
			},
		}

		inits = append(inits, initContainer)
	}

	return inits, nil
}

// daemonResolveCommand returns a shell command that resolves an artifact via
// the local artifact-daemon's /resolve endpoint. The daemon copies from the
// source path (identified by key) to the destination hostPath directory.
//
// key is the daemon-compatible artifact key (e.g. "producer-handle/result")
// which maps to steps/<key> on the daemon's filesystem.
// hostDest is the host filesystem path where the daemon should write the data.
func (c *Container) daemonResolveCommand(key, hostDest string) []string {
	if key == "" {
		script := `echo "ERROR: artifact key is empty — producing step did not record its output location" >&2; exit 1`
		return []string{"sh", "-c", script}
	}

	port := c.config.ArtifactDaemonPort
	if port == 0 {
		port = 8080
	}

	script := fmt.Sprintf(`
set -e
KEY="%s"
DST="%s"
PORT=%d
echo "[artifact-fetch] resolving key=${KEY} dest=${DST}" >&2
RESP=$(wget -qO- --post-data='{"key":"'"${KEY}"'","dest":"'"${DST}"'"}' "http://localhost:${PORT}/resolve" 2>&1) || {
  echo "[artifact-fetch] FAILED: ${RESP}" >&2
  exit 1
}
echo "[artifact-fetch] resolved: ${RESP}" >&2
`, key, hostDest, port)

	return []string{"sh", "-c", script}
}

// artifactLocate returns the full ArtifactLocation for a key.
func (c *Container) artifactLocate(key string) (ArtifactLocation, bool) {
	if c.artifactLocator == nil {
		return ArtifactLocation{}, false
	}
	return c.artifactLocator.Locate(key)
}

// artifactSourceNode returns the node name for an artifact key from the
// locator, or empty string if unknown.
func (c *Container) artifactSourceNode(key string) string {
	if c.artifactLocator == nil {
		return ""
	}
	node, _ := c.artifactLocator.LocateNode(key)
	return node
}

// buildSidecarContainers converts SidecarConfig entries into K8s container
// specs. Each sidecar receives the same volume mounts as the main container
// so it can access inputs, outputs, and caches.
func buildSidecarContainers(sidecars []atc.SidecarConfig, mainMounts []corev1.VolumeMount) []corev1.Container {
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

		c := corev1.Container{
			Name:            sc.Name,
			Image:           stripImagePrefix(sc.Image),
			Command:         sc.Command,
			Args:            sc.Args,
			WorkingDir:      sc.WorkingDir,
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

// buildAffinity constructs pod affinity rules when DaemonSet hostPath is configured.
// Returns nil when ArtifactDaemonHostPath is not set.
//
// - Hard affinity: pods MUST land on nodes with concourse.dev/artifact-cache=ready
// - Soft affinity: prefer the node that holds the most input artifacts
func (c *Container) buildAffinity() *corev1.Affinity {
	if c.config.ArtifactDaemonHostPath == "" {
		return nil
	}

	affinity := &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "concourse.dev/artifact-cache",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{"ready"},
							},
						},
					},
				},
			},
		},
	}

	// Add soft affinity for the node holding the most input artifacts.
	if c.artifactLocator != nil {
		preferredNode := c.preferredInputNode()
		if preferredNode != "" {
			affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{
				{
					Weight: 100,
					Preference: corev1.NodeSelectorTerm{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      "kubernetes.io/hostname",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{preferredNode},
							},
						},
					},
				},
			}
		}
	}

	return affinity
}

// preferredInputNode returns the node name that holds the most input
// artifacts, or empty string if no inputs have known locations.
func (c *Container) preferredInputNode() string {
	if c.artifactLocator == nil {
		return ""
	}

	counts := make(map[string]int)
	for _, input := range c.containerSpec.Inputs {
		if input.Artifact == nil {
			continue
		}
		key := ArtifactKey(input.Artifact.Handle())
		if node, ok := c.artifactLocator.LocateNode(key); ok {
			counts[node]++
		}
	}

	var best string
	var bestCount int
	for node, count := range counts {
		if count > bestCount {
			best = node
			bestCount = count
		}
	}
	return best
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
		addLabel("concourse.ci/build-id", strconv.Itoa(c.metadata.BuildID))
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
	return &corev1.PodSecurityContext{}
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
// stepVolume creates a volume for a step. When ArtifactDaemonHostPath is set,
// it creates a hostPath volume under <hostPath>/steps/<handle>/<subdir>/.
// Otherwise emptyDir.
func (c *Container) stepVolume(name, subdir string) corev1.Volume {
	if c.config.ArtifactDaemonHostPath != "" {
		dirType := corev1.HostPathDirectoryOrCreate
		return corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				HostPath: &corev1.HostPathVolumeSource{
					Path: filepath.Join(c.config.ArtifactDaemonHostPath, "steps", c.handle, subdir),
					Type: &dirType,
				},
			},
		}
	}
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}
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

	for _, input := range c.containerSpec.Inputs {
		name := fmt.Sprintf("input-%d", idx)
		idx++
		volumes = append(volumes, c.stepVolume(name, name))
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: input.DestinationPath,
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
		case c.config.ArtifactDaemonHostPath != "" && len(resolvedCaches) > 0:
			cacheMode = CacheStoreHostPath
		case c.config.CacheHostPath != "" && c.metadata.JobID != 0:
			cacheMode = CacheStoreHostPath
		default:
			cacheMode = CacheStoreEmptyDir
		}
	}

	switch cacheMode {
	case CacheStoreHostPath:
		// Use hostPath volumes with stable keys so caches persist across
		// builds on the same node.
		basePath := c.config.CacheHostPath
		if basePath == "" && c.config.ArtifactDaemonHostPath != "" {
			basePath = filepath.Join(c.config.ArtifactDaemonHostPath, "caches")
		}
		dirType := corev1.HostPathDirectoryOrCreate
		for i, cachePath := range resolvedCaches {
			key := stableCacheKey(c.metadata.JobID, c.metadata.StepName, cachePath)
			name := fmt.Sprintf("cache-%d", i)
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

	default: // CacheStoreEmptyDir or unknown
		// Ephemeral emptyDir volumes. Caches are lost on pod termination.
		for i, cachePath := range resolvedCaches {
			name := fmt.Sprintf("cache-%d", i)
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
