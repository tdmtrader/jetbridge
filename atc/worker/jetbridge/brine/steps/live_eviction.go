package steps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Write a fixed 24MiB into a 16MiB emptyDir. This is pod-local eviction,
// not node pressure, unbounded disk filling, or a supplied lifecycle status.
func observeLiveVolumeEviction(in LiveTaskPlan, rec *brine.Recorder) (StepOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec, 1)
	if err != nil {
		return StepOutcome{}, err
	}
	w.Executor = nil
	w = w.rebuild()
	const handle = "rf05-evicted"
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Create(w.Ctx, liveVolumeEvictionPod(handle, false), metav1.CreateOptions{})
	if err != nil {
		return StepOutcome{}, err
	}
	if original.UID == "" || original.ResourceVersion == "" {
		return StepOutcome{}, fmt.Errorf("eviction pod lacks API identity")
	}
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle),
		db.ContainerMetadata{Type: db.ContainerTypeTask},
		runtime.ContainerSpec{TeamID: w.TeamID, Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}}, nil)
	if err != nil {
		return StepOutcome{}, err
	}
	stderr := new(bytes.Buffer)
	process, err := container.Attach(w.Ctx, handle, runtime.ProcessIO{Stderr: stderr})
	if err != nil {
		return StepOutcome{}, err
	}
	if _, ok := process.(*jetbridge.Process); !ok {
		return StepOutcome{}, fmt.Errorf("expected direct compatibility Process, got %T", process)
	}
	if _, err := awaitLiveVolumeEviction(w, original); err != nil {
		return StepOutcome{}, err
	}
	ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
	defer cancel()
	result, waitErr := process.Wait(ctx)
	fmt.Printf("actual eviction result: process %T exit %d error %v\n", process, result.ExitStatus, waitErr)
	return StepOutcome{Err: waitErr, Message: errorMessage(waitErr), ExitStatus: result.ExitStatus, Stderr: stderr.String()}, nil
}

// One bounded writer serves compatibility diagnostics and pause recovery.
func liveVolumeEvictionPod(handle string, gated bool) *corev1.Pod {
	const volume = "scratch"
	limit := resource.MustParse("16Mi")
	grace := int64(1)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: handle},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			Volumes: []corev1.Volume{{Name: volume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}},
			Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37.0",
				Command:      []string{"sh", "-ec", "dd if=/dev/zero of=/scratch/payload bs=1048576 count=24 2>/dev/null; printf 'scratch-bytes='; wc -c < /scratch/payload; sleep 600"},
				VolumeMounts: []corev1.VolumeMount{{Name: volume, MountPath: "/scratch"}},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("32Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("32Mi")},
					Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("64Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("32Mi")},
				},
			}},
		},
	}
	if gated {
		pod.Spec.Containers[0].Command[2] = "while [ ! -f /tmp/brine-evict-go ]; do sleep 0.1; done; " + pod.Spec.Containers[0].Command[2]
	}
	return pod
}

// The bounded writer, actual volume-limit eviction and matching kubelet event
// establish the premise. Eviction may discard container logs/status before the
// first read; observing an intermediate Running state is not part of the contract.
func awaitLiveVolumeEviction(w WorkerReady, original *corev1.Pod) (*corev1.Pod, error) {
	handle := original.Name
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	var err error
	var actual *corev1.Pod
	for {
		actual, err = pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if actual.UID != original.UID {
			return nil, fmt.Errorf("eviction pod identity changed")
		}
		if actual.Status.Phase == corev1.PodFailed {
			fmt.Printf("actual eviction terminal container status: %+v\n", actual.Status.ContainerStatuses)
			if actual.Spec.NodeName == "" || actual.Spec.RestartPolicy != corev1.RestartPolicyNever || actual.Status.Reason != "Evicted" || !strings.Contains(actual.Status.Message, "scratch") || !strings.Contains(actual.Status.Message, "16Mi") {
				return nil, fmt.Errorf("expected actual volume-limit eviction after the bounded write, got %+v", actual.Status)
			}
			break
		}
		if actual.Status.Phase == corev1.PodSucceeded {
			return nil, fmt.Errorf("writer exited instead of being evicted")
		}
		select {
		case <-w.Ctx.Done():
			return nil, fmt.Errorf("kubelet did not evict oversized emptyDir: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	for {
		events, err := w.Clientset.CoreV1().Events(w.Namespace).List(w.Ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(original.UID)})
		if err != nil {
			return nil, err
		}
		witnessed := false
		for _, event := range events.Items {
			if event.InvolvedObject.UID == original.UID && event.Type == corev1.EventTypeWarning && event.Reason == "Evicted" &&
				(event.Source.Component == "kubelet" || event.ReportingController == "kubelet" || event.ReportingController == "kubernetes.io/kubelet") &&
				event.Message == actual.Status.Message {
				fmt.Printf("actual kubelet volume eviction: pod %s/%s UID %s RV %s node %s phase %s reason %s event %s message %q\n", w.Namespace, handle, actual.UID, actual.ResourceVersion, actual.Spec.NodeName, actual.Status.Phase, actual.Status.Reason, event.UID, event.Message)
				witnessed = true
				break
			}
		}
		if witnessed {
			break
		}
		select {
		case <-w.Ctx.Done():
			return nil, fmt.Errorf("no matching kubelet eviction event: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return actual, nil
}
