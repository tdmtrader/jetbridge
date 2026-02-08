package k8sruntime

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	exitStatusPropertyName = "concourse:exit-status"
	mainContainerName      = "main"
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
}

func newContainer(
	handle string,
	containerSpec runtime.ContainerSpec,
	dbContainer db.CreatedContainer,
	clientset kubernetes.Interface,
	config Config,
	workerName string,
	executor PodExecutor,
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
	}
}

func (c *Container) Run(ctx context.Context, spec runtime.ProcessSpec, io runtime.ProcessIO) (runtime.Process, error) {
	processID := c.handle
	if spec.ID != "" {
		processID = spec.ID
	}

	// Exec mode: when stdin is provided and we have an executor, create a
	// pause Pod and defer the actual command execution to Process.Wait.
	// This gives us full stdin/stdout/stderr separation via the exec API.
	if io.Stdin != nil && c.executor != nil {
		pod, err := c.createPausePod(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("create pause pod: %w", err)
		}
		return newExecProcess(processID, pod.Name, c.clientset, c.config, c, c.executor, spec, io), nil
	}

	// Direct mode: bake command into Pod spec.
	pod, err := c.createPod(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("create pod: %w", err)
	}

	return newProcess(processID, pod.Name, c.clientset, c.config, c), nil
}

func (c *Container) Attach(ctx context.Context, processID string, io runtime.ProcessIO) (runtime.Process, error) {
	// Check if the process has already exited (stored in properties).
	if statusStr, ok := c.properties[exitStatusPropertyName]; ok {
		status, err := strconv.Atoi(statusStr)
		if err == nil {
			return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
		}
	}

	return newProcess(processID, c.handle, c.clientset, c.config, c), nil
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
	image := c.containerSpec.ImageSpec.ImageURL
	if image == "" {
		image = c.containerSpec.ImageSpec.ResourceType
	}

	dir := processSpec.Dir
	if dir == "" {
		dir = c.containerSpec.Dir
	}

	env := envVars(c.containerSpec.Env)
	env = append(env, envVars(processSpec.Env)...)

	volumes, volumeMounts := c.buildVolumeMounts()

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
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes:       volumes,
			Containers: []corev1.Container{
				{
					Name:         mainContainerName,
					Image:        image,
					Command:      []string{processSpec.Path},
					Args:         processSpec.Args,
					WorkingDir:   dir,
					Env:          env,
					VolumeMounts: volumeMounts,
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
	image := c.containerSpec.ImageSpec.ImageURL
	if image == "" {
		image = c.containerSpec.ImageSpec.ResourceType
	}

	dir := processSpec.Dir
	if dir == "" {
		dir = c.containerSpec.Dir
	}

	env := envVars(c.containerSpec.Env)
	env = append(env, envVars(processSpec.Env)...)

	volumes, volumeMounts := c.buildVolumeMounts()

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
			RestartPolicy: corev1.RestartPolicyNever,
			Volumes:       volumes,
			Containers: []corev1.Container{
				{
					Name:         mainContainerName,
					Image:        image,
					Command:      []string{"sh", "-c", "trap 'exit 0' TERM; sleep 86400 & wait"},
					WorkingDir:   dir,
					Env:          env,
					VolumeMounts: volumeMounts,
				},
			},
		},
	}

	return c.clientset.CoreV1().Pods(c.config.Namespace).Create(ctx, pod, metav1.CreateOptions{})
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
