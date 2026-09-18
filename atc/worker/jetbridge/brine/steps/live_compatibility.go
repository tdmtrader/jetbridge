package steps

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type compatibilitySidecar struct {
	handle, name, image string
	// Non-nil runs a real one-shot sidecar before releasing the gated main.
	exitBeforeMain *int
	// A scenario-unique missing tag makes the kubelet supply a real pull refusal.
	pullFailure bool
}

// This explicitly exercises direct compatibility Process.Wait, not production
// execProcess. Production pod construction runs the command as PID 1; only the
// kubelet supplies its terminal phase and exit status. Observe that premise
// independently before letting the runtime interpret and clean up the pod.
func completeLiveCompatibility(in LiveTaskPlan, rec *brine.Recorder, code int, sidecars ...compatibilitySidecar) (ProcessOutcome, error) {
	if code < 0 || code > 255 {
		return ProcessOutcome{}, fmt.Errorf("invalid container exit code %d", code)
	}
	if len(sidecars) > 1 {
		return ProcessOutcome{}, fmt.Errorf("at most one compatibility sidecar is supported")
	}
	w, err := newLiveRuntimeWorker(in.Database, rec)
	if err != nil {
		return ProcessOutcome{}, err
	}
	executor := w.Executor
	w.Executor = nil
	w = w.rebuild()
	handle := "wait-success"
	phase := corev1.PodSucceeded
	if code != 0 {
		handle, phase = "wait-nonzero", corev1.PodFailed
	}
	command := "exit " + strconv.Itoa(code)
	var sidecar *compatibilitySidecar
	var configs []atc.SidecarConfig
	var probe []string
	var readyText string
	if len(sidecars) == 1 {
		sidecar = &sidecars[0]
		handle = sidecar.handle
		if sidecar.pullFailure {
			if sidecar.exitBeforeMain != nil {
				return ProcessOutcome{}, fmt.Errorf("pull refusal cannot also be a one-shot sidecar")
			}
			phase = corev1.PodPending
			sidecar.image = "busybox:" + w.Namespace + "-unavailable"
		} else if sidecar.exitBeforeMain == nil {
			phase = corev1.PodRunning
		}
		config := atc.SidecarConfig{Name: sidecar.name, Image: sidecar.image,
			Resources: &atc.SidecarResources{
				Requests: atc.SidecarResourceList{CPU: "50m", Memory: "32Mi"},
				Limits:   atc.SidecarResourceList{CPU: "250m", Memory: "256Mi"},
			}}
		if sidecar.exitBeforeMain != nil && (sidecar.image != "busybox:1.37.0" || *sidecar.exitBeforeMain < 0 || *sidecar.exitBeforeMain > 255) {
			return ProcessOutcome{}, fmt.Errorf("invalid one-shot compatibility sidecar")
		}
		switch {
		case sidecar.pullFailure:
			// Leave the command unchanged; no container starts for the missing image.
		case sidecar.image == "busybox:1.37.0":
			if sidecar.exitBeforeMain == nil {
				return ProcessOutcome{}, fmt.Errorf("one-shot sidecar needs an exit code")
			}
			config.Command = []string{"/bin/sh", "-c", "exit " + strconv.Itoa(*sidecar.exitBeforeMain)}
		case sidecar.image == "postgres:15":
			config.Env = []atc.SidecarEnvVar{{Name: "POSTGRES_PASSWORD", Value: w.Namespace + "-test-only"}}
			probe, readyText = []string{"pg_isready", "-h", "127.0.0.1", "-U", "postgres"}, "accepting connections"
		case sidecar.image == "redis:7":
			probe, readyText = []string{"redis-cli", "-h", "127.0.0.1", "ping"}, "PONG"
		default:
			return ProcessOutcome{}, fmt.Errorf("no service readiness probe for sidecar image %q", sidecar.image)
		}
		configs = []atc.SidecarConfig{config}
		if !sidecar.pullFailure {
			command = "while [ ! -f /tmp/brine-compat-exit ]; do sleep 0.1; done; " + command
		}
	}
	cpu, memory := uint64(250), uint64(64*1024*1024)
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask}, runtime.ContainerSpec{
			TeamID: w.TeamID, Type: db.ContainerTypeTask,
			ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"},
			Limits:    runtime.ContainerLimits{CPU: &cpu, Memory: &memory},
			Sidecars:  configs,
		}, nil)
	if err != nil {
		return ProcessOutcome{}, err
	}
	stderr := new(bytes.Buffer)
	process, err := container.Run(w.Ctx, runtime.ProcessSpec{
		Path: "/bin/sh", Args: []string{"-c", command},
	}, runtime.ProcessIO{Stderr: stderr})
	if err != nil {
		return ProcessOutcome{}, err
	}
	if _, ok := process.(*jetbridge.Process); !ok {
		return ProcessOutcome{}, fmt.Errorf("expected direct compatibility Process, got %T", process)
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
	if err != nil {
		return ProcessOutcome{}, err
	}
	sidecarID := ""
	var sidecarFinished metav1.Time
	probeSidecar := func() error {
		var output strings.Builder
		if err := executor.ExecInPod(w.Ctx, w.Namespace, handle, sidecar.name, probe,
			nil, &output, nil, false, jetbridge.ExecAttrs{Purpose: "compatibility-sidecar-readiness"}); err != nil {
			return fmt.Errorf("sidecar service not ready: %w", err)
		}
		if !strings.Contains(output.String(), readyText) {
			return fmt.Errorf("sidecar service response %q does not contain %q", output.String(), readyText)
		}
		return nil
	}
	if sidecar != nil && !sidecar.pullFailure {
		running, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, handle)
		if err != nil {
			return ProcessOutcome{}, err
		}
		if running.UID != original.UID {
			return ProcessOutcome{}, fmt.Errorf("compatibility pod was replaced before service readiness")
		}
		for {
			pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
			if err != nil {
				return ProcessOutcome{}, err
			}
			if pod.UID != original.UID || pod.Status.Phase != corev1.PodRunning {
				return ProcessOutcome{}, fmt.Errorf("compatibility pod left Running before main was released")
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name != sidecar.name {
					continue
				}
				if status.RestartCount != 0 || status.LastTerminationState.Terminated != nil {
					return ProcessOutcome{}, fmt.Errorf("sidecar restarted before main was released: %+v", status)
				}
				if sidecar.exitBeforeMain != nil {
					if dead := status.State.Terminated; dead != nil {
						if dead.ExitCode != int32(*sidecar.exitBeforeMain) || dead.ContainerID == "" || dead.StartedAt.IsZero() || dead.FinishedAt.IsZero() {
							return ProcessOutcome{}, fmt.Errorf("one-shot sidecar lost its actual exit: %+v", status)
						}
						sidecarID, sidecarFinished = dead.ContainerID, dead.FinishedAt
					}
					continue
				}
				if status.State.Terminated != nil {
					return ProcessOutcome{}, fmt.Errorf("sidecar ended before main was released: %+v", status)
				}
				if status.State.Running != nil && status.ContainerID != "" && status.ImageID != "" {
					if err := probeSidecar(); err == nil {
						sidecarID = status.ContainerID
					}
				}
			}
			if sidecarID != "" {
				if sidecar.exitBeforeMain != nil {
					mainRunning := false
					for _, status := range pod.Status.ContainerStatuses {
						mainRunning = mainRunning || status.Name == "main" && status.State.Running != nil
					}
					if !mainRunning {
						return ProcessOutcome{}, fmt.Errorf("main did not remain running behind its release gate")
					}
					fmt.Printf("real compatibility sidecar first: pod %s/%s UID %s sidecar %s container %s exited %d at %s while main remains gated\n",
						pod.Namespace, pod.Name, pod.UID, sidecar.name, sidecarID, *sidecar.exitBeforeMain, sidecarFinished.UTC().Format(time.RFC3339))
				} else {
					fmt.Printf("real compatibility sidecar: pod %s/%s UID %s sidecar %s image %s container %s responds %q before main exit\n",
						pod.Namespace, pod.Name, pod.UID, sidecar.name, sidecar.image, sidecarID, readyText)
				}
				break
			}
			select {
			case <-w.Ctx.Done():
				return ProcessOutcome{}, fmt.Errorf("wait for real sidecar service: %w", w.Ctx.Err())
			case <-time.After(250 * time.Millisecond):
			}
		}
		if err := executor.ExecInPod(w.Ctx, w.Namespace, handle, "main", []string{"touch", "/tmp/brine-compat-exit"},
			nil, nil, nil, false, jetbridge.ExecAttrs{Purpose: "release-compatibility-main"}); err != nil {
			return ProcessOutcome{}, err
		}
	}
	for {
		pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return ProcessOutcome{}, err
		}
		if pod.UID == "" || pod.UID != original.UID {
			return ProcessOutcome{}, fmt.Errorf("compatibility completion pod identity changed")
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "main" || status.State.Terminated == nil {
				continue
			}
			// Container termination can be published before the pod phase.
			// Wait for the kubelet to supply the original terminal premise.
			if (sidecar == nil || sidecar.exitBeforeMain != nil) && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				continue
			}
			if sidecar != nil && sidecar.pullFailure {
				ready, err := observeCompatibilityPullRefusal(w, pod, *sidecar)
				if err != nil {
					return ProcessOutcome{}, err
				}
				if !ready {
					continue
				}
			}
			dead := status.State.Terminated
			if pod.Status.Phase != phase || pod.Spec.NodeName == "" || pod.Spec.RestartPolicy != corev1.RestartPolicyNever ||
				status.RestartCount != 0 || status.LastTerminationState.Terminated != nil ||
				dead.ExitCode != int32(code) || dead.ContainerID == "" || dead.StartedAt.IsZero() || dead.FinishedAt.IsZero() {
				return ProcessOutcome{}, fmt.Errorf("actual completion lost its terminal/no-restart premise: %+v", pod.Status)
			}
			if sidecar != nil && sidecar.exitBeforeMain != nil {
				mainIndex, sidecarIndex := -1, -1
				for i, status := range pod.Status.ContainerStatuses {
					if status.Name == "main" {
						mainIndex = i
					}
					if status.Name == sidecar.name && status.State.Terminated != nil {
						ended := status.State.Terminated
						if ended.ContainerID != sidecarID || ended.ExitCode != int32(*sidecar.exitBeforeMain) ||
							!ended.FinishedAt.Equal(&sidecarFinished) || status.RestartCount != 0 || status.LastTerminationState.Terminated != nil {
							return ProcessOutcome{}, fmt.Errorf("completed sidecar identity or result changed")
						}
						sidecarIndex = i
					}
				}
				if sidecarIndex < 0 || mainIndex <= sidecarIndex || dead.FinishedAt.Before(&sidecarFinished) {
					return ProcessOutcome{}, fmt.Errorf("kubelet status lost sidecar-first ordering: %+v", pod.Status)
				}
				fmt.Printf("real compatibility exit ordering: pod %s/%s UID %s sidecar index %d exit %d precedes main index %d exit %d; phase %s\n",
					pod.Namespace, pod.Name, pod.UID, sidecarIndex, *sidecar.exitBeforeMain, mainIndex, code, pod.Status.Phase)
			}
			if sidecar != nil && sidecar.exitBeforeMain == nil && !sidecar.pullFailure {
				stillRunning := false
				for _, status := range pod.Status.ContainerStatuses {
					stillRunning = stillRunning || status.Name == sidecar.name && status.ContainerID == sidecarID &&
						status.State.Running != nil && status.RestartCount == 0 && status.LastTerminationState.Terminated == nil
				}
				if !stillRunning {
					return ProcessOutcome{}, fmt.Errorf("main completed without the same running sidecar: %+v", pod.Status)
				}
				if err := probeSidecar(); err != nil {
					return ProcessOutcome{}, err
				}
				fmt.Printf("real compatibility sidecar: pod %s/%s UID %s sidecar %s container %s still Running and responding after main exit %d\n",
					pod.Namespace, pod.Name, pod.UID, sidecar.name, sidecarID, code)
			}
			fmt.Printf("real compatibility completion: pod %s/%s UID %s node %s phase %s current exit %d container %s; no restart history\n",
				pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, pod.Status.Phase, dead.ExitCode, dead.ContainerID)
			ctx, cancel := context.WithTimeout(w.Ctx, 10*time.Second)
			result, waitErr := process.Wait(ctx)
			cancel()
			return ProcessOutcome{
				Ctx: w.Ctx, Namespace: w.Namespace, Clientset: w.Clientset, Handle: handle,
				podUID: string(pod.UID), NodeName: pod.Spec.NodeName,
				ExitStatus: result.ExitStatus, Err: waitErr, Message: errorMessage(waitErr), Stderr: stderr.String(),
			}, nil
		}
		select {
		case <-w.Ctx.Done():
			return ProcessOutcome{}, fmt.Errorf("wait for actual compatibility completion: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Observe both the current kubelet refusal and its independent pull event.
// No phase, container status or registry response is manufactured.
func observeCompatibilityPullRefusal(w WorkerReady, pod *corev1.Pod, sidecar compatibilitySidecar) (bool, error) {
	var waiting *corev1.ContainerStateWaiting
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != sidecar.name {
			continue
		}
		if status.State.Running != nil || status.State.Terminated != nil ||
			status.RestartCount != 0 || status.LastTerminationState.Terminated != nil {
			return false, fmt.Errorf("unavailable sidecar unexpectedly ran: %+v", status)
		}
		waiting = status.State.Waiting
	}
	if waiting == nil || waiting.Reason != "ImagePullBackOff" {
		return false, nil
	}
	if !strings.Contains(waiting.Message, sidecar.image) {
		return false, fmt.Errorf("kubelet refusal omits requested image %q: %q", sidecar.image, waiting.Message)
	}
	events, err := w.Clientset.CoreV1().Events(w.Namespace).List(w.Ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.uid=" + string(pod.UID),
	})
	if err != nil {
		return false, err
	}
	for _, event := range events.Items {
		if event.InvolvedObject.UID != pod.UID || event.InvolvedObject.FieldPath != "spec.containers{"+sidecar.name+"}" ||
			event.Reason != "Failed" || event.Type != corev1.EventTypeWarning ||
			(event.Source.Component != "kubelet" && event.ReportingController != "kubelet" && event.ReportingController != "kubernetes.io/kubelet") ||
			!strings.Contains(event.Message, "Failed to pull image") || !strings.Contains(event.Message, sidecar.image) {
			continue
		}
		fmt.Printf("real compatibility pull refusal: pod %s/%s UID %s sidecar %s image %s event %s message %q; no restart history\n",
			pod.Namespace, pod.Name, pod.UID, sidecar.name, sidecar.image, event.UID, event.Message)
		return true, nil
	}
	return false, nil
}
