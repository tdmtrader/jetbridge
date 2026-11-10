package jetbridge

// The job step runtime (Config.StepRuntime == StepRuntimeJob).
//
// A step is one suspended batch/v1 Job whose pod template carries the step's
// command in its own spec, so admission control sees exactly what runs. The
// main container's entrypoint is the launcher, a static binary an init
// container copies from the jetbridge image into an emptyDir; the launcher
// runs the command, forwards signals to its process group and exits with its
// code. Nothing is ever exec'd into the pod.
//
// Unsuspending the Job starts the step. The exit code is the main container's
// terminated status. Logs are read with pods/log. A resource command's answer
// (its stdout) is written by the launcher into the step's answer directory on
// the node's artifact host path and read back through the artifact daemon.
// Its request JSON, which carries interpolated source and params, never
// appears in a Kubernetes object: the launcher fetches it as the step
// envelope, presenting a projected ServiceAccount token bound to its pod.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

const (
	// launcherVolumeName and launcherMountPath are the emptyDir the launcher
	// init container copies jb-launcher into, mounted read-only in main.
	launcherVolumeName = "jb-launcher"
	launcherMountPath  = "/jb"
	launcherBinary     = "/usr/local/concourse/bin/jb-launcher"
	launcherInitName   = "jb-launcher"

	// stepTokenVolumeName mounts the projected ServiceAccount token the
	// launcher presents when fetching the step envelope. Its audience is
	// nothing the API server accepts, so the token grants no Kubernetes
	// access: the default step pod identity stays permissionless.
	stepTokenVolumeName   = "jb-step-token"
	stepTokenMountPath    = "/var/run/jetbridge"
	stepTokenFile         = "token"
	StepTokenAudience     = "jetbridge-step"
	stepTokenExpirySecond = 600

	// answerVolumeName is the host-path directory a resource command's
	// stdout is written to: <artifact host path>/steps/<handle>/answer,
	// which the artifact daemon serves under the key steps/<handle>/answer.
	answerVolumeName = "jb-answer"
	answerMountPath  = "/jb/answer"
	answerSubdir     = "answer"
	answerFile       = "stdout"

	// runtimeLabelKey marks a pod built by the job runtime. A web that finds
	// a step pod without it was built by the exec runtime.
	runtimeLabelKey = "concourse.ci/runtime"

	// jobNameLabelKey and jobUIDLabelKey are the labels the Job controller
	// stamps on the pods it creates. The web finds a step Job's pod by the
	// UID: a Job of the same name that went before may still have a pod the
	// garbage collector has not taken, and that pod is not this step's.
	jobNameLabelKey = "batch.kubernetes.io/job-name"
	jobUIDLabelKey  = "batch.kubernetes.io/controller-uid"

	// stepJobTerminationGrace is how long the launcher's process group has to
	// finish after SIGTERM before the kubelet kills it. It covers the
	// launcher's own kill grace.
	stepJobTerminationGrace int64 = 30

	// stepJobPodAppearTimeout bounds how long the web waits for the Job
	// controller to create the pod after the Job is unsuspended.
	stepJobPodAppearTimeout = 2 * time.Minute
)

// exactExecution reports whether this container's step is a Run-owned exact
// execution: admitted under an execution control envelope on a worker whose
// output plane is reachable. Such a step still runs on the exec runtime,
// unless a test routed an exact task to the job runtime.
func (c *Container) exactExecution() bool {
	return c.containerSpec.ExecutionControl != nil && c.outputControls != nil
}

// jobRuntime reports whether this container runs its step as a Job.
func (c *Container) jobRuntime() bool {
	return c.config.StepRuntime == StepRuntimeJob && !c.lookedUp && (!c.exactExecution() || c.exactTaskOnJobRuntime())
}

// exactTaskOnJobRuntime reports whether this container is an exact task a
// test routed to the job runtime. Only a test binary can route one: nothing
// in this package sets exactTasksOnJobRuntime, and the setters live in _test
// files (exact_job_routing_test.go, live_exact_controls_test.go). Only a task:
// an exact check, get or put stays on the pause pod either way.
func (c *Container) exactTaskOnJobRuntime() bool {
	return c.exactTasksOnJobRuntime && c.exactExecution() && c.metadata.Type == db.ContainerTypeTask
}

// stepAnswerKey is the artifact key the daemon serves a step's answer
// directory under.
func stepAnswerKey(handle string) string {
	return "steps/" + handle + "/" + answerSubdir
}

// buildStepJob composes the suspended Job for one step. The pod template is
// composePod's pod with the launcher in front of the command, the launcher init
// and its emptyDir, the projected token (when the step has an envelope), the
// answer directory (for a resource command) and the declared sidecars as
// native sidecars, which end when main does. The finished template passes the
// step pod admission once, under the allowance composePod resolved: it is the
// job runtime's only admission.
func (c *Container) buildStepJob(spec runtime.ProcessSpec, hasEnvelope bool) (*batchv1.Job, error) {
	if c.config.LauncherImage == "" {
		return nil, errors.New("job step runtime: no launcher image configured")
	}
	launcherArgs := []string{}
	if hasEnvelope {
		if c.config.StepEnvelopeURL == "" {
			return nil, errors.New("job step runtime: no step envelope URL configured")
		}
		launcherArgs = append(launcherArgs,
			"--envelope-url="+c.config.StepEnvelopeURL,
			"--token-file="+filepath.Join(stepTokenMountPath, stepTokenFile))
	}
	resource := c.metadata.Type == db.ContainerTypeCheck || c.metadata.Type == db.ContainerTypeGet || c.metadata.Type == db.ContainerTypePut
	if resource {
		launcherArgs = append(launcherArgs, "--stdout-file="+filepath.Join(answerMountPath, answerFile))
	}
	launcherArgs = append(launcherArgs, "--")
	launcherArgs = append(launcherArgs, spec.Path)
	launcherArgs = append(launcherArgs, spec.Args...)

	pod, allowance, err := c.composePod(spec, []string{filepath.Join(launcherMountPath, "jb-launcher")}, launcherArgs)
	if err != nil {
		return nil, err
	}
	pod.Labels[runtimeLabelKey] = StepRuntimeJob

	allowEscalation := false
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         launcherVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
	launcherInit := corev1.Container{
		Name:            launcherInitName,
		Image:           c.config.LauncherImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"cp", launcherBinary, filepath.Join(launcherMountPath, "jb-launcher")},
		VolumeMounts:    []corev1.VolumeMount{{Name: launcherVolumeName, MountPath: launcherMountPath}},
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowEscalation},
	}
	if resource {
		// The kubelet creates the answer directory root-owned; the launcher
		// init, which runs as the jetbridge image's root, opens it so a
		// resource image running as another user can write its answer.
		launcherInit.Command = []string{"sh", "-ec", fmt.Sprintf("cp %s %s && chmod 1777 %s",
			launcherBinary, filepath.Join(launcherMountPath, "jb-launcher"), answerMountPath)}
		launcherInit.VolumeMounts = append(launcherInit.VolumeMounts, corev1.VolumeMount{Name: answerVolumeName, MountPath: answerMountPath})
	}
	// The launcher copy goes after every other init: the cleanup init
	// recreates the step directory, so the answer directory it opens must
	// be opened after that.
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, launcherInit)

	main := &pod.Spec.Containers[0]
	// The step's command and env now reach the kubelet in the pod spec rather
	// than through exec, and the kubelet expands $(VAR) and collapses $$ in
	// both. Doubling every $ makes it hand the launcher the text as written.
	for i := range main.Args {
		main.Args[i] = escapeKubernetesExpansion(main.Args[i])
	}
	for i := range main.Env {
		main.Env[i].Value = escapeKubernetesExpansion(main.Env[i].Value)
	}
	main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: launcherVolumeName, MountPath: launcherMountPath, ReadOnly: true})
	if hasEnvelope {
		expiry := int64(stepTokenExpirySecond)
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: stepTokenVolumeName,
			VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
					Audience:          StepTokenAudience,
					ExpirationSeconds: &expiry,
					Path:              stepTokenFile,
				}}},
			}},
		})
		main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: stepTokenVolumeName, MountPath: stepTokenMountPath, ReadOnly: true})
	}
	if resource {
		if c.config.ArtifactDaemonHostPath == "" {
			return nil, errors.New("job step runtime: a resource command's answer needs the artifact daemon host path")
		}
		dirType := corev1.HostPathDirectoryOrCreate
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: answerVolumeName,
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: filepath.Join(stepHostPath(c.config.ArtifactDaemonHostPath, c.handle), answerSubdir),
				Type: &dirType,
			}},
		})
		main.VolumeMounts = append(main.VolumeMounts, corev1.VolumeMount{Name: answerVolumeName, MountPath: answerMountPath})
	}
	// A task's command ran on a TTY under the exec runtime; keeping one
	// keeps its output the same (colour, width) without any exec session.
	if spec.TTY != nil {
		main.TTY = true
	}

	// Declared sidecars become native sidecars: init containers with
	// restartPolicy Always, placed after every ordinary init so they start
	// once the inputs are staged, and stopped by the kubelet once main
	// exits. A Job's pod would otherwise never complete.
	if len(pod.Spec.Containers) > 1 {
		always := corev1.ContainerRestartPolicyAlways
		for _, sidecar := range pod.Spec.Containers[1:] {
			sidecar.RestartPolicy = &always
			pod.Spec.InitContainers = append(pod.Spec.InitContainers, sidecar)
		}
		pod.Spec.Containers = pod.Spec.Containers[:1]
	}
	pod.Spec.TerminationGracePeriodSeconds = new(int64)
	*pod.Spec.TerminationGracePeriodSeconds = stepJobTerminationGrace
	if err := validatePodVolumeMounts(pod); err != nil {
		return nil, err
	}
	// The finished template is the job runtime's one admission: the answer
	// directory's host path and the privilege gate, under the allowance
	// composePod resolved with the identity, so the build is looked up once.
	if err := admitStepPod(pod, c.stepPodRoots(), allowance); err != nil {
		return nil, err
	}

	suspend := true
	one := int32(1)
	zero := int32(0)
	// A disrupted pod (eviction, preemption, node drain) fails the Job
	// outright rather than being replaced: a replacement would run the
	// command a second time.
	failurePolicy := &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{{
		Action:          batchv1.PodFailurePolicyActionFailJob,
		OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}},
	}}}
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        c.podName,
			Namespace:   c.config.Namespace,
			Labels:      pod.Labels,
			Annotations: pod.Annotations,
		},
		Spec: batchv1.JobSpec{
			Suspend:          &suspend,
			Parallelism:      &one,
			Completions:      &one,
			BackoffLimit:     &zero,
			PodFailurePolicy: failurePolicy,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: pod.Labels, Annotations: pod.Annotations},
				Spec:       pod.Spec,
			},
		},
	}
	return job, nil
}

// createStepJob creates the suspended Job, then unsuspends it, and reports
// whether it created one.
//
// A Job of this name may exist already. If its pod is running, it is this
// step's, started by a web that died before it could follow it: it is left
// alone and followed, and this invocation's envelope is not registered, the
// running pod already has its own. If its pod has finished, or it never got a
// pod (a web that died between creating and unsuspending it), it is a
// previous invocation on this handle (a repeated check reuses its container)
// or a start that never happened: the Job is deleted and a new one created,
// as the exec runtime replaces a terminal pause pod.
func (c *Container) createStepJob(ctx context.Context, job *batchv1.Job) (bool, error) {
	logger := lagerctx.FromContext(ctx).Session("create-step-job", lagerData(c))
	for attempt := 0; ; attempt++ {
		created, err := c.clientset.BatchV1().Jobs(c.config.Namespace).Create(ctx, job, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			if attempt > 0 {
				return false, fmt.Errorf("step job %q still exists after its deletion", job.Name)
			}
			finished, err := c.stepJobFinished(ctx)
			if err != nil {
				return false, err
			}
			if !finished {
				logger.Info("following-existing-step-job")
				return false, nil
			}
			logger.Info("replacing-finished-step-job")
			if err := c.replaceFinishedStepJob(ctx); err != nil {
				return false, err
			}
			continue
		}
		if err != nil {
			return false, fmt.Errorf("create step job: %w", err)
		}
		if created.Spec.Suspend == nil || !*created.Spec.Suspend {
			return false, errors.New("step job was not created suspended")
		}
		c.mu.Lock()
		c.jobUID = created.UID
		c.jobPodName = ""
		c.mu.Unlock()
		patch := []byte(`{"spec":{"suspend":false}}`)
		if _, err := c.clientset.BatchV1().Jobs(c.config.Namespace).Patch(ctx, job.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return false, fmt.Errorf("unsuspend step job: %w", err)
		}
		return true, nil
	}
}

// stepJobFinished reports whether the existing Job of this step's name has
// run its course, or never started: its pod is terminal, the Job itself is
// complete or failed, or it is still suspended with no pod, which no web is
// about to unsuspend.
func (c *Container) stepJobFinished(ctx context.Context) (bool, error) {
	existing, err := c.stepJob(ctx)
	if err != nil {
		return false, fmt.Errorf("reading the existing step job: %w", err)
	}
	pod, err := findPodOfJob(ctx, c.clientset, c.config.Namespace, c.podName, existing.UID)
	if err == nil {
		_, done := podExitCode(pod)
		return done, nil
	}
	if !errors.Is(err, errJobPodNotFound) {
		return false, err
	}
	for _, condition := range existing.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true, nil
		}
	}
	return existing.Spec.Suspend != nil && *existing.Spec.Suspend, nil
}

// replaceFinishedStepJob deletes the finished Job and waits for its name and
// its pod to be gone, so the pod that follows is a new one and the name is
// free.
func (c *Container) replaceFinishedStepJob(ctx context.Context) error {
	c.mu.RLock()
	uid := c.jobUID
	c.mu.RUnlock()
	if err := deleteStepJob(ctx, c.clientset, c.config.Namespace, c.podName, ""); err != nil {
		return err
	}
	c.mu.Lock()
	c.jobPodName, c.jobUID = "", ""
	c.mu.Unlock()
	deadline := time.Now().Add(time.Minute)
	for {
		_, err := c.clientset.BatchV1().Jobs(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
		jobGone := apierrors.IsNotFound(err)
		if err != nil && !jobGone {
			return fmt.Errorf("waiting for the finished step job to go: %w", err)
		}
		_, err = findPodOfJob(ctx, c.clientset, c.config.Namespace, c.podName, uid)
		podGone := errors.Is(err, errJobPodNotFound)
		if jobGone && podGone {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the finished step job %q (job gone: %v, pod gone: %v) is still present a minute after its deletion", c.podName, jobGone, podGone)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// deleteStepJob deletes a step's Job and, through it, its pod. A step pod
// is never deleted on its own: the Job controller would create another, and
// that would run the command a second time. Background propagation: the
// call returns at once and the pod receives its termination grace.
//
// With a uid, only the Job of that UID is deleted. A Job recreated under the
// same name is another step's: the API server refuses the delete with a
// Conflict, which is taken as the Job being gone already -- as it is, and a
// pod it owned goes with it through the garbage collector.
func deleteStepJob(ctx context.Context, clientset kubernetes.Interface, namespace, name string, uid types.UID) error {
	propagation := metav1.DeletePropagationBackground
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation}
	if uid != "" {
		opts.Preconditions = metav1.NewUIDPreconditions(string(uid))
	}
	err := clientset.BatchV1().Jobs(namespace).Delete(ctx, name, opts)
	if apierrors.IsNotFound(err) || (uid != "" && apierrors.IsConflict(err)) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete step job %s: %w", name, err)
	}
	return nil
}

// owningStepJob is the name of the step Job that owns a pod, or "" for a pod
// no Job owns (an exec-runtime pause pod).
func owningStepJob(pod metav1.ObjectMeta) string {
	name, _ := owningStepJobRef(pod)
	return name
}

// owningStepJobRef is the name and UID of the step Job that owns a pod.
func owningStepJobRef(pod metav1.ObjectMeta) (string, types.UID) {
	for _, owner := range pod.OwnerReferences {
		if owner.Kind == "Job" && owner.APIVersion == "batch/v1" {
			return owner.Name, owner.UID
		}
	}
	return "", ""
}

// deleteStepPod deletes a step pod the right way for its runtime: through
// its Job when one owns it, directly otherwise. The Job is deleted only if
// it is still the one the pod's owner reference names.
func deleteStepPod(ctx context.Context, clientset kubernetes.Interface, namespace string, pod metav1.ObjectMeta, opts metav1.DeleteOptions) error {
	if job, uid := owningStepJobRef(pod); job != "" {
		return deleteStepJob(ctx, clientset, namespace, job, uid)
	}
	return clientset.CoreV1().Pods(namespace).Delete(ctx, pod.Name, opts)
}

// errJobPodNotFound is the error findJobPod returns while the Job controller has
// not created the pod yet.
var errJobPodNotFound = errors.New("step job has no pod yet")

// stepJob reads the step's Job, remembering its UID.
func (c *Container) stepJob(ctx context.Context) (*batchv1.Job, error) {
	job, err := c.clientset.BatchV1().Jobs(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.jobUID = job.UID
	c.mu.Unlock()
	return job, nil
}

// findJobPod returns the pod the step's Job owns: the pod labelled with the
// Job's UID, not merely its name. A Job with backoffLimit 0 and one
// completion owns at most one pod; a second one is reported rather than
// chosen between, because two pods means the command may have run twice.
func (c *Container) findJobPod(ctx context.Context) (*corev1.Pod, error) {
	c.mu.RLock()
	uid := c.jobUID
	c.mu.RUnlock()
	if uid == "" {
		job, err := c.stepJob(ctx)
		if apierrors.IsNotFound(err) {
			return nil, errJobPodNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("reading the step job: %w", err)
		}
		uid = job.UID
	}
	return findPodOfJob(ctx, c.clientset, c.config.Namespace, c.podName, uid)
}

func findPodOfJob(ctx context.Context, clientset kubernetes.Interface, namespace, name string, uid types.UID) (*corev1.Pod, error) {
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: jobUIDLabelKey + "=" + string(uid),
	})
	if err != nil {
		return nil, fmt.Errorf("list step job pods: %w", err)
	}
	switch len(pods.Items) {
	case 0:
		return nil, errJobPodNotFound
	case 1:
		return &pods.Items[0], nil
	default:
		names := make([]string, 0, len(pods.Items))
		for _, pod := range pods.Items {
			names = append(names, pod.Name)
		}
		return nil, fmt.Errorf("step job %q owns %d pods %v: the command may have run more than once", name, len(pods.Items), names)
	}
}

// awaitJobPod waits for the Job controller to create the step's pod.
func (c *Container) awaitJobPod(ctx context.Context) (*corev1.Pod, error) {
	deadline := time.Now().Add(stepJobPodAppearTimeout)
	wait := 200 * time.Millisecond
	for {
		pod, err := c.findJobPod(ctx)
		if err == nil {
			c.mu.Lock()
			c.jobPodName = pod.Name
			c.mu.Unlock()
			return pod, nil
		}
		if !errors.Is(err, errJobPodNotFound) {
			return nil, err
		}
		if time.Now().After(deadline) {
			job, getErr := c.clientset.BatchV1().Jobs(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
			if getErr != nil {
				return nil, fmt.Errorf("step job %q has no pod after %s and cannot be read: %w", c.podName, stepJobPodAppearTimeout, getErr)
			}
			return nil, fmt.Errorf("step job %q has no pod after %s (active %d, suspended %v)", c.podName, stepJobPodAppearTimeout, job.Status.Active, job.Spec.Suspend != nil && *job.Spec.Suspend)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		if wait < 2*time.Second {
			wait *= 2
		}
	}
}

// resolvePodName is the name of the pod this container's step runs in. Under
// the exec runtime that is the pod the container named; under the job
// runtime it is the Job's pod, found once and remembered. A looked-up
// container (fly intercept) on a job-runtime worker may name either: a
// Job's pod when one exists, else the pause pod of an exact execution.
func (c *Container) resolvePodName(ctx context.Context) (string, error) {
	c.mu.RLock()
	name := c.jobPodName
	c.mu.RUnlock()
	if name != "" {
		return name, nil
	}
	if !c.jobRuntime() && !(c.lookedUp && c.config.StepRuntime == StepRuntimeJob) {
		return c.podName, nil
	}
	pod, err := c.findJobPod(ctx)
	if err != nil {
		if c.lookedUp && errors.Is(err, errJobPodNotFound) {
			return c.podName, nil
		}
		return "", err
	}
	c.mu.Lock()
	c.jobPodName = pod.Name
	c.mu.Unlock()
	return pod.Name, nil
}

// forgetEnvelope drops the step's envelope once its command is over.
func (c *Container) forgetEnvelope() {
	if c.envelopes != nil {
		c.envelopes.Forget(c.podName)
	}
}

func lagerData(c *Container) map[string]any {
	return map[string]any{"handle": c.handle, "job": c.podName}
}

// runAsJob starts the step as a Job. The envelope (the command's stdin) is
// registered for the Job's pod to fetch before the Job is unsuspended, so
// the pod never asks for something that is not there. A Job already running
// keeps the envelope its pod has.
func (c *Container) runAsJob(ctx context.Context, spec runtime.ProcessSpec, pio runtime.ProcessIO) (runtime.Process, error) {
	processID := c.handle
	if spec.ID != "" {
		processID = spec.ID
	}
	var stdin []byte
	if pio.Stdin != nil {
		read, err := io.ReadAll(pio.Stdin)
		if err != nil {
			return nil, fmt.Errorf("reading the step's stdin: %w", err)
		}
		stdin = read
	}
	// An exact task has a life of its own here: its envelope fetch is its
	// start gate, and a Job of its name already there is resumed, never
	// replaced (exact_job_wait.go).
	if c.exactTaskOnJobRuntime() {
		return c.runExactAsJob(ctx, processID, spec, pio, stdin)
	}
	hasEnvelope := pio.Stdin != nil
	if hasEnvelope && c.envelopes == nil {
		return nil, errors.New("job step runtime: no step envelope store; this worker cannot run a command that reads stdin")
	}
	job, err := c.buildStepJob(spec, hasEnvelope)
	if err != nil {
		return nil, err
	}
	if hasEnvelope {
		c.envelopes.Register(c.podName, StepEnvelope{Stdin: stdin})
	}
	created, err := c.createStepJob(ctx, job)
	if err != nil {
		if hasEnvelope {
			c.envelopes.Forget(c.podName)
		}
		metric.Metrics.FailedContainers.Inc()
		return nil, wrapIfTransient(err)
	}
	if !created && hasEnvelope {
		// The running pod took its own envelope; this invocation's answer is
		// that command's. A later web resuming the step registers the same
		// envelope again, which is what a pod that has not fetched yet needs.
		c.envelopes.Forget(c.podName)
		c.envelopes.Register(c.podName, StepEnvelope{Stdin: stdin})
	}
	metric.Metrics.ContainersCreated.Inc()
	// The deferred volumes' exec destination is vestigial here: nothing
	// streams through exec under the job runtime.
	c.bindVolumesToPod(c.podName)
	return newJobProcess(processID, c, pio), nil
}

// attachJob resumes a step Job after a web restart. A finished step is
// answered from its pod: the exit-status annotation, or failing that main's
// terminated status, which the kubelet keeps for as long as the pod exists.
// A step still running, or one whose Job the Job controller has not given a
// pod yet, is answered with an error so the engine falls through to Run,
// which finds the existing Job and follows it.
func (c *Container) attachJob(ctx context.Context, processID string) (runtime.Process, error) {
	pod, err := c.findJobPod(ctx)
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	c.mu.Lock()
	c.jobPodName = pod.Name
	c.mu.Unlock()
	if statusStr, ok := pod.Annotations[exitStatusAnnotationKey]; ok {
		if status, err := strconv.Atoi(statusStr); err == nil {
			c.forgetEnvelope()
			return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: status}}, nil
		}
	}
	// An exact task's exit is its result only once the ledger holds it, and
	// the annotation above is written only then. Recovering it from the pod
	// is not done yet (exact_job_process.go).
	if c.exactTaskOnJobRuntime() {
		return nil, fmt.Errorf("attach: exact step job %q has no acknowledged outcome to report", c.podName)
	}
	if exit, done := podExitCode(pod); done {
		c.SetProperty(exitStatusPropertyName, strconv.Itoa(exit))
		c.forgetEnvelope()
		return &exitedProcess{id: processID, result: runtime.ProcessResult{ExitStatus: exit}}, nil
	}
	return nil, fmt.Errorf("attach: step job %q is still running in pod %q", c.podName, pod.Name)
}

// WebImage is the image the web runs: the container of its own pod that
// runs the `web` command. Under the job step runtime the launcher is copied
// out of it, so the launcher is always the web's own build.
func WebImage(ctx context.Context, clientset kubernetes.Interface, namespace, podName string) (string, error) {
	if podName == "" {
		return "", errors.New("no web pod name")
	}
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("reading pod %s/%s: %w", namespace, podName, err)
	}
	for _, container := range pod.Spec.Containers {
		if len(container.Args) > 0 && container.Args[0] == "web" {
			return container.Image, nil
		}
	}
	if len(pod.Spec.Containers) == 1 {
		return pod.Spec.Containers[0].Image, nil
	}
	return "", fmt.Errorf("pod %s/%s has no container running the web command", namespace, podName)
}

// escapeKubernetesExpansion returns s with every $ doubled, so the kubelet's
// $(VAR) expansion of container args and env values yields s unchanged.
func escapeKubernetesExpansion(s string) string {
	return strings.ReplaceAll(s, "$", "$$")
}
