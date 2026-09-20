package steps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	"github.com/concourse/concourse/vars"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// The kubelet, not a status setter, supplies the original Pending/current-waiting
// premise. This explicitly exercises the direct compatibility Process; it does
// not claim a task command executed or that production uses that fallback.
func diagnoseLiveStartup(in LiveTaskPlan, rec *brine.Recorder, cause string) (StepOutcome, error) {
	out, err := diagnoseLiveStartupProcess(in, rec, cause, "direct compatibility", "main")
	return StepOutcome{Err: out.Err, Message: out.Message, ExitStatus: out.ExitStatus, Stderr: out.Stderr}, err
}

func diagnoseLiveStartupProcess(in LiveTaskPlan, rec *brine.Recorder, cause, mode, target string) (ProcessOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec)
	if err != nil {
		return ProcessOutcome{}, err
	}
	if target != "main" && target != "my-sidecar" {
		return ProcessOutcome{}, fmt.Errorf("unknown failed container %q", target)
	}
	switch mode {
	case "direct compatibility":
		w.Executor = nil
	case "production":
		// Keep the real production SPDY executor.
	default:
		return ProcessOutcome{}, fmt.Errorf("unknown execution mode %q", mode)
	}
	w = w.rebuild()
	cpu, memory := uint64(250), uint64(64*1024*1024)
	spec := runtime.ContainerSpec{
		TeamID: w.TeamID, Type: db.ContainerTypeTask,
		ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"},
		Limits:    runtime.ContainerLimits{CPU: &cpu, Memory: &memory},
	}
	var handle, expected, pullImage string
	switch cause {
	case "an invalid image name":
		handle, expected = "rf04-invalidname", "InvalidImageName"
		spec.ImageSpec.ImageURL = "docker:///not a valid image"
	case "a failed image pull", "image pull backoff":
		handle, expected = "rf04-errpull", "ErrImagePull"
		if cause == "image pull backoff" {
			handle, expected = "rf04-pullbackoff", "ImagePullBackOff"
		}
		// A valid, scenario-unique tag triggers an actual registry pull.
		// No response or kubelet status is synthesized.
		pullImage = "busybox:" + w.Namespace + "-unavailable"
		spec.ImageSpec.ImageURL = "docker:///" + pullImage
	case "a missing required secret":
		handle, expected = "rf04-configerr", "CreateContainerConfigError"
		const secret = "missing-startup-secret"
		if _, err := w.Clientset.CoreV1().Secrets(w.Namespace).Get(w.Ctx, secret, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			return ProcessOutcome{}, fmt.Errorf("expected owned namespace to lack required secret %q: %v", secret, err)
		}
		spec.Env = []string{"REQUIRED_VALUE=unused"}
		spec.SecretEnv = map[string]vars.SecretRef{
			"REQUIRED_VALUE": {Name: secret, Key: "value"},
		}
	default:
		return ProcessOutcome{}, fmt.Errorf("unknown real startup failure %q", cause)
	}
	if target != "main" {
		if cause != "image pull backoff" {
			return ProcessOutcome{}, fmt.Errorf("sidecar requires an actual pull backoff")
		}
		spec.ImageSpec.ImageURL = "docker:///busybox:1.37.0"
		spec.Sidecars = []atc.SidecarConfig{{Name: target, Image: pullImage}}
	}
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx,
		db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypeTask}, spec, nil)
	if err != nil {
		return ProcessOutcome{}, err
	}
	stderr := new(bytes.Buffer)
	command := runtime.ProcessSpec{Path: "/bin/true"}
	if target != "main" {
		command = runtime.ProcessSpec{Path: "/bin/sh", Args: []string{"-c", "sleep 600"}}
	}
	process, err := container.Run(w.Ctx, command, runtime.ProcessIO{Stderr: stderr})
	if err != nil {
		return ProcessOutcome{}, fmt.Errorf("create real startup pod: %w", err)
	}
	if mode == "direct compatibility" {
		if _, ok := process.(*jetbridge.Process); !ok {
			return ProcessOutcome{}, fmt.Errorf("expected compatibility Process, got %T", process)
		}
	} else if fmt.Sprintf("%T", process) != "*jetbridge.execProcess" {
		return ProcessOutcome{}, fmt.Errorf("expected production execProcess, got %T", process)
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if pullImage != "" {
		matched := false
		for _, c := range original.Spec.Containers {
			matched = matched || c.Name == target && c.Image == pullImage
		}
		if !matched {
			return ProcessOutcome{}, fmt.Errorf("runtime pod did not request image %q", pullImage)
		}
	}
	for {
		pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return ProcessOutcome{}, err
		}
		if pod.UID == "" || pod.UID != original.UID {
			return ProcessOutcome{}, fmt.Errorf("startup pod identity changed")
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != target {
				continue
			}
			if status.State.Running != nil || status.State.Terminated != nil {
				return ProcessOutcome{}, fmt.Errorf("invalid task unexpectedly started: %+v", status)
			}
			if status.State.Waiting == nil || status.State.Waiting.Reason != expected {
				continue
			}
			if target != "main" {
				mainUnfinished := false
				for _, other := range pod.Status.ContainerStatuses {
					if other.Name == "main" && other.State.Terminated == nil && (other.State.Running != nil || other.State.Waiting != nil) {
						mainUnfinished = true
					}
				}
				if !mainUnfinished {
					return ProcessOutcome{}, fmt.Errorf("sidecar refusal requires an unfinished actual main container")
				}
			}
			if pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName == "" ||
				status.LastTerminationState.Terminated != nil || status.RestartCount != 0 || status.State.Waiting.Message == "" {
				return ProcessOutcome{}, fmt.Errorf("kubelet failure did not preserve Pending/no-restart premise: %+v", pod.Status)
			}
			if pullImage != "" {
				scheduled := false
				for _, cond := range pod.Status.Conditions {
					scheduled = scheduled || cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionTrue
				}
				if !scheduled {
					return ProcessOutcome{}, fmt.Errorf("pull refusal has no successful scheduling condition")
				}
			}
			if pullImage != "" && !strings.Contains(status.State.Waiting.Message, pullImage) {
				return ProcessOutcome{}, fmt.Errorf("kubelet refusal does not identify the requested image %q: %q", pullImage, status.State.Waiting.Message)
			}
			fmt.Printf("real startup refusal: pod %s/%s UID %s node %s phase %s reason %s message %q; no restart history\n",
				pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, pod.Status.Phase, status.State.Waiting.Reason, status.State.Waiting.Message)
			// Bound a broken fail-fast classifier independently of startup.
			if pullImage != "" {
				metric.Metrics.K8sImagePullFailures.Delta()
			}
			waitCtx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
			result, waitErr := process.Wait(waitCtx)
			cancel()
			var failures float64
			if pullImage != "" {
				failures = metric.Metrics.K8sImagePullFailures.Delta()
			}
			if pullImage != "" {
				// Events independently witness an actual kubelet pull attempt.
				// They remain readable even if a false-success mutation deletes
				// the pod; match its UID, container, reporter and exact image.
				for {
					events, err := w.Clientset.CoreV1().Events(w.Namespace).List(w.Ctx, metav1.ListOptions{
						FieldSelector: "involvedObject.uid=" + string(original.UID),
					})
					if err != nil {
						return ProcessOutcome{}, err
					}
					witnessed := false
					for _, event := range events.Items {
						if event.InvolvedObject.UID != original.UID || event.InvolvedObject.FieldPath != "spec.containers{"+target+"}" ||
							event.Type != corev1.EventTypeWarning || event.Reason != "Failed" ||
							(event.Source.Component != "kubelet" && event.ReportingController != "kubelet" && event.ReportingController != "kubernetes.io/kubelet") ||
							!strings.Contains(event.Message, "Failed to pull image") || !strings.Contains(event.Message, pullImage) {
							continue
						}
						fmt.Printf("real image-pull refusal: pod %s/%s UID %s image %s event %s reason %s message %q\n",
							w.Namespace, handle, original.UID, pullImage, event.UID, event.Reason, event.Message)
						witnessed = true
						break
					}
					if witnessed {
						break
					}
					select {
					case <-w.Ctx.Done():
						return ProcessOutcome{}, fmt.Errorf("no actual kubelet image-pull failure event: %w", w.Ctx.Err())
					case <-time.After(100 * time.Millisecond):
					}
				}
			}
			return ProcessOutcome{Ctx: w.Ctx, Namespace: w.Namespace, Clientset: w.Clientset,
				Handle: handle, podUID: string(original.UID), NodeName: pod.Spec.NodeName,
				ImagePullFailures: failures, FailedContainer: target,
				Err: waitErr, Message: errorMessage(waitErr), ExitStatus: result.ExitStatus, Stderr: stderr.String()}, nil
		}
		select {
		case <-w.Ctx.Done():
			return ProcessOutcome{}, fmt.Errorf("wait for actual kubelet %s: %w", expected, w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func diagnoseLiveStartupTimeout(in LiveTaskPlan, rec *brine.Recorder) (ProcessOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec)
	if err != nil {
		return ProcessOutcome{}, err
	}
	const handle = "startup-timeout"
	initial, initName, err := prepareLiveInitPod(w, handle, false)
	if err != nil {
		return ProcessOutcome{}, err
	}
	// Preparation polls through the observer client. Give the runtime its own
	// normal API client so that polling does not consume its 200ms read budget.
	config, err := liveKubernetesConfig()
	if err != nil {
		return ProcessOutcome{}, err
	}
	runtimeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ProcessOutcome{}, err
	}
	w.Clientset = runtimeClient
	// Keep the original production deadlines. Physical scheduling and init
	// startup happen before Wait; only the runtime's deadline is under test.
	w.Config.PodStartupTimeout, w.Config.PodSchedulingTimeout = 200*time.Millisecond, 200*time.Millisecond
	w = w.rebuild()
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx,
		db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}}, nil)
	if err != nil {
		return ProcessOutcome{}, err
	}
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{Path: "/bin/true"}, runtime.ProcessIO{Stdout: stdout, Stderr: stderr})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if fmt.Sprintf("%T", process) != "*jetbridge.execProcess" {
		return ProcessOutcome{}, fmt.Errorf("expected production execProcess, got %T", process)
	}
	ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
	start := time.Now()
	result, waitErr := process.Wait(ctx)
	elapsed := time.Since(start)
	cancel()
	pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if pod.UID != initial.UID || pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != initial.Spec.NodeName ||
		len(pod.Status.InitContainerStatuses) != 1 || pod.Status.InitContainerStatuses[0].Name != initName ||
		pod.Status.InitContainerStatuses[0].State.Running == nil ||
		pod.Status.InitContainerStatuses[0].ContainerID != initial.Status.InitContainerStatuses[0].ContainerID ||
		pod.Status.InitContainerStatuses[0].RestartCount != 0 || stdout.Len() != 0 {
		return ProcessOutcome{}, fmt.Errorf("real init gate did not preserve scheduled Pending through Wait: %+v", pod.Status)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running != nil || status.State.Terminated != nil {
			return ProcessOutcome{}, fmt.Errorf("container ran despite the unreleased init gate: %+v", status)
		}
	}
	fmt.Printf("real startup timeout: pod %s/%s UID %s node %s phase %s init %s container %s remains Running; runtime %T Wait %s; no main execution\n",
		pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, pod.Status.Phase, initName,
		pod.Status.InitContainerStatuses[0].ContainerID, process, elapsed)
	return ProcessOutcome{Ctx: w.Ctx, Namespace: w.Namespace, Clientset: w.Clientset, Handle: handle,
		podUID: string(pod.UID), NodeName: pod.Spec.NodeName,
		ExitStatus: result.ExitStatus, Err: waitErr, Message: errorMessage(waitErr), Stderr: stderr.String()}, nil
}
