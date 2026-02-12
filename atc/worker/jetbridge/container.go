package jetbridge

import (
	"context"
	"encoding/json"
	"fmt"
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
	cachePVCVolumeName          = "cache-pvc"

	// gcsFuseAnnotationKey is the pod annotation required by the GKE
	// GCS Fuse sidecar injector webhook. When set to "true", the webhook
	// injects the FUSE mount helper into the pod.
	gcsFuseAnnotationKey = "gke-gcsfuse/volumes"
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
	executor      PodExecutor
	volumes       []*Volume
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
) *Container {
	return &Container{
		handle:        handle,
		podName:       GeneratePodName(metadata, handle),
		metadata:      metadata,
		containerSpec: containerSpec,
		dbContainer:   dbContainer,
		clientset:     clientset,
		config:        config,
		workerName:    workerName,
		properties:    make(map[string]string),
		executor:      executor,
		volumes:       volumes,
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
	pod := c.buildPod(processSpec, []string{processSpec.Path}, processSpec.Args)
	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// pauseCommand is the shell command used by pause pods. It sleeps
// indefinitely and exits cleanly on SIGTERM so the pod can be stopped.
const pauseCommand = "trap 'exit 0' TERM; sleep 86400 & wait"

// createPausePod creates a Pod that runs indefinitely (pause mode) so that
// Process.Wait can exec the real command via the PodExecutor with full
// stdin/stdout/stderr support.
func (c *Container) createPausePod(ctx context.Context, processSpec runtime.ProcessSpec) (*corev1.Pod, error) {
	pod := c.buildPod(processSpec, []string{"sh", "-c", pauseCommand}, nil)
	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// buildPod constructs a Pod spec with the given command and args. All other
// fields (image, env, volumes, security, etc.) are derived from the
// Container's spec and config.
func (c *Container) buildPod(processSpec runtime.ProcessSpec, command []string, args []string) *corev1.Pod {
	image := resolveImage(c.containerSpec.ImageSpec, c.config.ResourceTypeImages)

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

	initContainers := c.buildArtifactInitContainers(volumeMounts)

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

	if sidecar := c.buildArtifactHelperSidecar(volumeMounts); sidecar != nil {
		containers = append(containers, *sidecar)
	}

	containers = append(containers, buildSidecarContainers(c.containerSpec.Sidecars, volumeMounts)...)

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
		},
	}
}

// buildArtifactStoreVolume returns a PVC volume for the artifact store, or nil
// if ArtifactStoreClaim is not configured.
func (c *Container) buildArtifactStoreVolume() *corev1.Volume {
	if c.config.ArtifactStoreClaim == "" {
		return nil
	}
	if c.metadata.Type == db.ContainerTypeCheck {
		return nil
	}
	return &corev1.Volume{
		Name: artifactPVCVolumeName,
		VolumeSource: corev1.VolumeSource{
			PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: c.config.ArtifactStoreClaim,
			},
		},
	}
}

// buildArtifactInitContainers creates init containers for each input with a
// non-nil artifact when ArtifactStoreClaim is configured. Each init container
// extracts a tar file from the artifact PVC into the corresponding emptyDir
// volume. The artifact key is derived from the artifact's Handle(), which
// works for both *Volume (same-build) and *ArtifactStoreVolume (cross-build).
func (c *Container) buildArtifactInitContainers(mainMounts []corev1.VolumeMount) []corev1.Container {
	if c.config.ArtifactStoreClaim == "" {
		return nil
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

		inits = append(inits, corev1.Container{
			Name:  fmt.Sprintf("fetch-input-%d", i),
			Image: helperImage,
			Command: []string{"sh", "-c",
				fmt.Sprintf("tar xf %s/%s -C %s",
					ArtifactMountPath, key, input.DestinationPath),
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: artifactPVCVolumeName, MountPath: ArtifactMountPath, ReadOnly: true},
				{Name: volumeName, MountPath: input.DestinationPath},
			},
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &allowEscalation,
			},
		})
	}
	return inits
}

// buildArtifactHelperSidecar creates the artifact-helper sidecar container
// that shares output emptyDir volumes and the artifact PVC. It runs a pause
// command; the ATC execs tar upload commands in it after step completion.
// Returns nil if ArtifactStoreClaim is not configured.
func (c *Container) buildArtifactHelperSidecar(mainMounts []corev1.VolumeMount) *corev1.Container {
	if c.config.ArtifactStoreClaim == "" {
		return nil
	}
	// Check steps never produce artifacts — skip the sidecar to reduce
	// per-check resource overhead and avoid triggering GCS FUSE injection.
	if c.metadata.Type == db.ContainerTypeCheck {
		return nil
	}

	helperImage := c.config.ArtifactHelperImage
	if helperImage == "" {
		helperImage = DefaultArtifactHelperImage
	}

	// The sidecar mounts the same volumes as the main container plus the
	// artifact PVC. This allows it to read output data from emptyDir and
	// write tars to the PVC.
	allMounts := make([]corev1.VolumeMount, len(mainMounts))
	copy(allMounts, mainMounts)
	allMounts = append(allMounts, corev1.VolumeMount{
		Name:      artifactPVCVolumeName,
		MountPath: ArtifactMountPath,
	})

	allowEscalation := false
	return &corev1.Container{
		Name:            artifactHelperContainerName,
		Image:           helperImage,
		Command:         []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
		VolumeMounts:    allMounts,
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: &allowEscalation,
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}
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
			Image:           sc.Image,
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
	addLabel(handleLabelKey, c.handle)

	return labels
}

// buildPodAnnotations returns annotations for the pod. When the artifact
// store PVC is backed by GCS Fuse, includes the annotation required by
// the GKE sidecar injector webhook.
func (c *Container) buildPodAnnotations() map[string]string {
	annotations := map[string]string{}
	if c.config.ArtifactStoreGCSFuse && c.config.ArtifactStoreClaim != "" && c.metadata.Type != db.ContainerTypeCheck {
		annotations[gcsFuseAnnotationKey] = "true"
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
	image := spec.ImageURL

	// Strip common Concourse image URL prefixes.
	for _, prefix := range []string{"docker:///", "docker://", "raw:///"} {
		if strings.HasPrefix(image, prefix) {
			image = strings.TrimPrefix(image, prefix)
			break
		}
	}

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
// requirements. CPU is mapped to millicores, Memory to bytes. When limits
// are specified, requests are set equal to limits to ensure Guaranteed QoS.
// When no limits are specified, returns an empty ResourceRequirements
// (BestEffort QoS).
func buildResourceRequirements(limits runtime.ContainerLimits) corev1.ResourceRequirements {
	reqs := corev1.ResourceRequirements{}
	if limits.CPU == nil && limits.Memory == nil {
		return reqs
	}

	res := corev1.ResourceList{}
	if limits.CPU != nil {
		res[corev1.ResourceCPU] = *resource.NewMilliQuantity(int64(*limits.CPU), resource.DecimalSI)
	}
	if limits.Memory != nil {
		res[corev1.ResourceMemory] = *resource.NewQuantity(int64(*limits.Memory), resource.BinarySI)
	}

	reqs.Limits = res
	reqs.Requests = res
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

// buildVolumeMounts creates K8s Volume and VolumeMount entries for
// the container's Dir, inputs, outputs, and caches.
func (c *Container) buildVolumeMounts() ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	idx := 0

	if c.containerSpec.Dir != "" {
		name := fmt.Sprintf("dir-%d", idx)
		idx++
		volumes = append(volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: c.containerSpec.Dir,
		})
	}

	for _, input := range c.containerSpec.Inputs {
		name := fmt.Sprintf("input-%d", idx)
		idx++
		volumes = append(volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: input.DestinationPath,
		})
	}

	// Sort output names for deterministic ordering.
	outputNames := make([]string, 0, len(c.containerSpec.Outputs))
	for name := range c.containerSpec.Outputs {
		outputNames = append(outputNames, name)
	}
	sort.Strings(outputNames)

	for _, outputName := range outputNames {
		path := c.containerSpec.Outputs[outputName]
		name := fmt.Sprintf("output-%d", idx)
		idx++
		volumes = append(volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name,
			MountPath: path,
		})
	}

	if c.config.CacheVolumeClaim != "" {
		// When a cache PVC is configured, mount it into the pod and use
		// subPath mounts for each cache entry so data survives pod restarts.
		volumes = append(volumes, corev1.Volume{
			Name: cachePVCVolumeName,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: c.config.CacheVolumeClaim,
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      cachePVCVolumeName,
			MountPath: CacheBasePath,
		})

		for i, cachePath := range c.containerSpec.Caches {
			cacheHandle := fmt.Sprintf("%s-cache-%d", c.handle, i)
			mounts = append(mounts, corev1.VolumeMount{
				Name:      cachePVCVolumeName,
				MountPath: cachePath,
				SubPath:   cacheHandle,
			})
		}
	} else {
		for i, cachePath := range c.containerSpec.Caches {
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

	return volumes, mounts
}
