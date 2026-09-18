package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A scheduling gate prevents every container from starting. After deletion
// is requested, the actual PodGC controller supplies Failed without container
// status. An owned finalizer holds that state for the real compatibility
// Process.Wait, then cleanup removes only that finalizer and verifies deletion.
// This tests the phase fallback, not command execution or node disruption.
func observeControllerFailedPod(in LiveTaskPlan, rec *brine.Recorder) (StepOutcome, error) {
	w, err := newLiveRuntimeWorker(in.Database, rec, 1)
	if err != nil {
		return StepOutcome{}, err
	}
	w.Executor = nil
	w = w.rebuild()
	const handle = "no-status-failed"
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	original, err := pods.Create(w.Ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: handle}, Spec: corev1.PodSpec{
		RestartPolicy:   corev1.RestartPolicyNever,
		SchedulingGates: []corev1.PodSchedulingGate{{Name: startupSchedulingGate}},
		Containers:      []corev1.Container{{Name: "main", Image: "busybox:1.37.0", Command: []string{"true"}}},
	}}, metav1.CreateOptions{})
	if err != nil {
		return StepOutcome{}, err
	}

	// Registered before the finalizer release so LIFO cleanup releases first,
	// then deletes/verifies the exact pod, even if setup fails before deletion.
	rec.RegisterDisposer(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := pods.Delete(ctx, handle, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &original.UID}}); err != nil && !apierrors.IsNotFound(err) {
			panic(err)
		}
		for {
			pod, err := pods.Get(ctx, handle, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				fmt.Printf("removed owned terminal-observation pod %s UID %s\n", handle, original.UID)
				return
			}
			if err != nil {
				panic(err)
			}
			if pod.UID != original.UID {
				panic("terminal-observation pod UID changed")
			}
			select {
			case <-ctx.Done():
				panic(fmt.Errorf("terminal-observation pod cleanup: %w", ctx.Err()))
			case <-time.After(100 * time.Millisecond):
			}
		}
	})
	if _, err := holdLivePodDeletion(w, original, rec); err != nil {
		return StepOutcome{}, err
	}
	container, _, err := w.Worker.FindOrCreateContainer(w.Ctx, db.NewFixedHandleContainerOwner(handle), db.ContainerMetadata{Type: db.ContainerTypeTask}, runtime.ContainerSpec{TeamID: w.TeamID, Type: db.ContainerTypeTask, ImageSpec: runtime.ImageSpec{ImageURL: "docker:///busybox:1.37.0"}}, nil)
	if err != nil {
		return StepOutcome{}, err
	}
	process, err := container.Attach(w.Ctx, handle, runtime.ProcessIO{})
	if err != nil {
		return StepOutcome{}, err
	}
	if _, ok := process.(*jetbridge.Process); !ok {
		return StepOutcome{}, fmt.Errorf("expected compatibility Process, got %T", process)
	}
	if err := pods.Delete(w.Ctx, handle, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &original.UID}}); err != nil {
		return StepOutcome{}, err
	}
	for {
		pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return StepOutcome{}, err
		}
		if pod.UID != original.UID || pod.Spec.NodeName != "" || len(pod.Status.ContainerStatuses) != 0 || len(pod.Status.InitContainerStatuses) != 0 {
			return StepOutcome{}, fmt.Errorf("controller fallback lost its unstarted, unassigned pod premise: %+v", pod.Status)
		}
		if pod.Status.Phase == corev1.PodFailed {
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue {
					return StepOutcome{}, fmt.Errorf("controller produced an interruption rather than the plain phase fallback: %+v", c)
				}
			}
			if pod.DeletionTimestamp == nil {
				return StepOutcome{}, fmt.Errorf("controller Failed observation was not terminating")
			}
			fmt.Printf("actual controller failure: pod %s/%s UID %s RV %s phase Failed, no node or container statuses, deletion timestamp %s\n", w.Namespace, handle, pod.UID, pod.ResourceVersion, pod.DeletionTimestamp)
			ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Second)
			defer cancel()
			result, waitErr := process.Wait(ctx)
			fmt.Printf("actual no-status fallback: process %T exit=%d error=%v\n", process, result.ExitStatus, waitErr)
			return StepOutcome{Err: waitErr, Message: errorMessage(waitErr), ExitStatus: result.ExitStatus}, nil
		}
		select {
		case <-w.Ctx.Done():
			return StepOutcome{}, fmt.Errorf("wait for real controller Failed state: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
