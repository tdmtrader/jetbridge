package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Shared with the exec-interruption/current-OOM case. Only the priority case
// restarts: its emptyDir preserves the arming gate across container deaths.
// Every allocation is bounded by an independently read 64 MiB cgroup limit.
func liveOOMPod(handle string, restart corev1.RestartPolicy) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: handle},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{
			Name: "main", Image: "busybox:1.37.0",
			Command: []string{"sh", "-c", "while [ ! -f /tmp/brine-oom-go ]; do sleep 0.1; done; sleep 1; exec awk 'BEGIN { s=sprintf(\"%1048576s\",\"\"); for(i=0;i<256;i++) a[i]=s i; exit 9 }'"},
		}, {
			// Keep the pod Running after main dies, preserving the original
			// current-termination diagnostic premise without setting status.
			Name: "keep-running", Image: "busybox:1.37.0",
			Command: []string{"sh", "-c", "trap 'exit 0' TERM; while :; do sleep 1; done"},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5m"), corev1.ResourceMemory: resource.MustParse("4Mi")},
				Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")},
			},
		}}},
	}
	pod.Spec.RestartPolicy = restart
	if restart == corev1.RestartPolicyAlways {
		pod.Spec.Volumes = []corev1.Volume{{Name: "oom-gate", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "oom-gate", MountPath: "/tmp"}}
		// Verify inside each restarted container as well as before arming.
		pod.Spec.Containers[0].Command[2] = "if [ -f /sys/fs/cgroup/memory.max ]; then limit=$(cat /sys/fs/cgroup/memory.max); else limit=$(cat /sys/fs/cgroup/memory/memory.limit_in_bytes); fi; [ \"$limit\" = 67108864 ] || exit 10; " + pod.Spec.Containers[0].Command[2]
	}
	return pod
}

// Read actual simultaneous CrashLoopBackOff/current-waiting and OOMKilled/last
// termination. This is the direct compatibility Process's priority contract;
// production task execution still uses execProcess.
func diagnoseLiveOOMPriority(in LiveTaskPlan, rec *brine.Recorder) (StepOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec)
	if err != nil {
		return StepOutcome{}, err
	}
	direct, ok := w.Executor.(*jetbridge.SPDYExecutor)
	if !ok {
		return StepOutcome{}, fmt.Errorf("expected real SPDY executor, got %T", w.Executor)
	}
	w.Executor = nil
	w = w.rebuild()
	const handle = "rf09-oom-vs-crash"
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{TeamID: w.TeamID, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}}, nil)
	if err != nil {
		return StepOutcome{}, err
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Create(w.Ctx, liveOOMPod(handle, corev1.RestartPolicyAlways), metav1.CreateOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	armed := false
	var lastObserved *corev1.Pod
	for {
		pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return StepOutcome{}, describeOOMObservation(armed, lastObserved, err)
		}
		lastObserved = pod
		if pod.UID == "" || pod.UID != original.UID {
			return StepOutcome{}, fmt.Errorf("OOM priority pod identity changed")
		}
		sidecarRunning := false
		for _, status := range pod.Status.ContainerStatuses {
			sidecarRunning = sidecarRunning || status.Name == "keep-running" && status.State.Running != nil
		}
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "main" {
				continue
			}
			if !armed && status.State.Running != nil {
				if status.RestartCount != 0 || status.LastTerminationState.Terminated != nil || status.ContainerID == "" {
					return StepOutcome{}, fmt.Errorf("OOM priority pod restarted before arming: %+v", status)
				}
				if err := armLiveTaskMemory(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, direct, pod); err != nil {
					return StepOutcome{}, err
				}
				armed = true
			}
			if !armed || status.State.Waiting == nil || status.State.Waiting.Reason != "CrashLoopBackOff" {
				continue
			}
			last := status.LastTerminationState.Terminated
			if pod.Status.Phase != corev1.PodRunning || pod.Spec.NodeName == "" || !sidecarRunning ||
				status.State.Running != nil || status.State.Terminated != nil ||
				status.RestartCount < 1 || status.State.Waiting.Message == "" ||
				last == nil || last.Reason != "OOMKilled" || last.ExitCode != 137 || last.ContainerID == "" {
				return StepOutcome{}, fmt.Errorf("actual crash loop lost the Running/last-OOM premise: %+v", pod.Status)
			}
			fmt.Printf("real OOM priority: pod %s/%s UID %s node %s phase %s waiting %s last %s exit %d container %s restarts %d; sidecar Running; cgroup 67108864 verified\n",
				pod.Namespace, pod.Name, pod.UID, pod.Spec.NodeName, pod.Status.Phase, status.State.Waiting.Reason, last.Reason, last.ExitCode, last.ContainerID, status.RestartCount)
			ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
			defer cancel()
			stderr := new(bytes.Buffer)
			process, err := container.Attach(ctx, handle, runtime.ProcessIO{Stderr: stderr})
			if err != nil {
				return StepOutcome{Err: err, Message: errorMessage(err)}, nil
			}
			if _, ok := process.(*jetbridge.Process); !ok {
				return StepOutcome{}, fmt.Errorf("expected direct compatibility Process, got %T", process)
			}
			result, waitErr := process.Wait(ctx)
			return StepOutcome{Err: waitErr, Message: errorMessage(waitErr), Stderr: stderr.String(), ExitStatus: result.ExitStatus}, nil
		}
		select {
		case <-w.Ctx.Done():
			return StepOutcome{}, describeOOMObservation(armed, lastObserved, w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func describeOOMObservation(armed bool, pod *corev1.Pod, err error) error {
	if pod == nil {
		return fmt.Errorf("OOM priority ended before any pod read: %w", err)
	}
	status, marshalErr := json.Marshal(pod.Status)
	if marshalErr != nil {
		return fmt.Errorf("OOM diagnostic serialization: %v; observation: %w", marshalErr, err)
	}
	return fmt.Errorf("OOM priority ended: armed=%t node=%q status=%s: %w", armed, pod.Spec.NodeName, status, err)
}
