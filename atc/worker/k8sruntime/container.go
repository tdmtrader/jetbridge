package k8sruntime

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	exitStatusPropertyName   = "concourse:exit-status"
	mainContainerName        = "main"
	exitStatusAnnotationKey  = "concourse.ci/exit-status"
)

// Compile-time check that Container satisfies runtime.Container.
var _ runtime.Container = (*Container)(nil)

// Container implements runtime.Container backed by a Kubernetes Pod.
// The Pod is created lazily when Run() is called, since the command
// (ProcessSpec) isn't known at FindOrCreateContainer time.
type Container struct {
	handle        string
	containerSpec runtime.ContainerSpec
	dbContainer   db.CreatedContainer
	clientset     kubernetes.Interface
	config        Config
	workerName    string
	properties    map[string]string
	executor      PodExecutor
	volumes       []*Volume
}

func newContainer(
	handle string,
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
	processID := c.handle
	if spec.ID != "" {
		processID = spec.ID
	}

	// Exec mode: use a pause Pod and exec the real command via SPDY.
	// If the pod already exists (e.g. fly hijack into an existing container),
	// reuse it. Otherwise create a new pause pod.
	if c.executor != nil {
		podName := c.handle
		_, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.handle, metav1.GetOptions{})
		if err != nil {
			// Pod doesn't exist — create a new pause pod.
			pod, createErr := c.createPausePod(ctx, spec)
			if createErr != nil {
				return nil, fmt.Errorf("create pause pod: %w", createErr)
			}
			podName = pod.Name
		}
		c.bindVolumesToPod(podName)
		return newExecProcess(processID, podName, c.clientset, c.config, c, c.executor, spec, io), nil
	}

	// Fallback direct mode: only used when no executor is configured
	// (e.g. tests that don't set up SPDY). Bakes command into Pod spec.
	pod, err := c.createPod(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("create pod: %w", err)
	}
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
	// Check if the process has already exited (stored in properties).
	if statusStr, ok := c.properties[exitStatusPropertyName]; ok {
		status, err := strconv.Atoi(statusStr)
		if err == nil {
			return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
		}
	}

	// Check whether the Pod actually exists in K8s. If it does not, return
	// an error so that attachOrRun falls through to Run() which creates
	// the Pod.
	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.handle, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("attach: pod %q not found: %w", c.handle, err)
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
		return nil, fmt.Errorf("attach: exec-mode pod %q has no completion status", c.handle)
	}

	return newProcess(processID, c.handle, c.clientset, c.config, c, io), nil
}

func (c *Container) Properties() (map[string]string, error) {
	return c.properties, nil
}

func (c *Container) SetProperty(name string, value string) error {
	c.properties[name] = value
	return nil
}

func (c *Container) DBContainer() db.CreatedContainer {
	return c.dbContainer
}

func (c *Container) createPod(ctx context.Context, processSpec runtime.ProcessSpec) (*corev1.Pod, error) {
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

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.handle,
			Namespace: c.config.Namespace,
			Labels: map[string]string{
				"concourse.ci/worker": c.workerName,
				"concourse.ci/type":   string(c.containerSpec.Type),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			SecurityContext:    buildPodSecurityContext(privileged),
			ImagePullSecrets:   buildImagePullSecrets(c.config.ImagePullSecrets),
			ServiceAccountName: c.config.ServiceAccount,
			Volumes:            volumes,
			Containers: []corev1.Container{
				{
					Name:            mainContainerName,
					Image:           image,
					Command:         []string{processSpec.Path},
					Args:            processSpec.Args,
					WorkingDir:      dir,
					Env:             env,
					VolumeMounts:    volumeMounts,
					Resources:       resources,
					SecurityContext: buildContainerSecurityContext(privileged),
					ImagePullPolicy: corev1.PullIfNotPresent,
				},
			},
		},
	}

	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
}

// createPausePod creates a Pod that runs indefinitely (pause mode) so that
// Process.Wait can exec the real command via the PodExecutor with full
// stdin/stdout/stderr support.
func (c *Container) createPausePod(ctx context.Context, processSpec runtime.ProcessSpec) (*corev1.Pod, error) {
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

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      c.handle,
			Namespace: c.config.Namespace,
			Labels: map[string]string{
				"concourse.ci/worker": c.workerName,
				"concourse.ci/type":   string(c.containerSpec.Type),
			},
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			SecurityContext:    buildPodSecurityContext(privileged),
			ImagePullSecrets:   buildImagePullSecrets(c.config.ImagePullSecrets),
			ServiceAccountName: c.config.ServiceAccount,
			Volumes:            volumes,
			Containers: []corev1.Container{
				{
					Name:            mainContainerName,
					Image:           image,
					Command:         []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
					WorkingDir:      dir,
					Env:             env,
					VolumeMounts:    volumeMounts,
					Resources:       resources,
					SecurityContext: buildContainerSecurityContext(privileged),
					ImagePullPolicy: corev1.PullIfNotPresent,
				},
			},
		},
	}

	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
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
// LocalObjectReference entries for the pod spec.
func buildImagePullSecrets(secretNames []string) []corev1.LocalObjectReference {
	var refs []corev1.LocalObjectReference
	for _, name := range secretNames {
		refs = append(refs, corev1.LocalObjectReference{Name: name})
	}
	return refs
}

// buildVolumeMounts creates K8s Volume and VolumeMount entries for
// the container's inputs, outputs, and caches.
func (c *Container) buildVolumeMounts() ([]corev1.Volume, []corev1.VolumeMount) {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	idx := 0

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

	return volumes, mounts
}
