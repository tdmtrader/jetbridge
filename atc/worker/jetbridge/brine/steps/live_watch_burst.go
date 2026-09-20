package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The scheduling gate preserves the initial Pending read. A file gate keeps
// the actual main process Running until the independent observer releases it.
func newLiveGatedWatch(rec *brine.Recorder, name string) (RealWatch, error) {
	return newLiveGatedWatchWithTimeout(rec, name, 90*time.Second)
}

func newLiveGatedWatchWithTimeout(rec *brine.Recorder, name string, timeout time.Duration) (RealWatch, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	rec.RegisterDisposer(cancel)
	cluster, err := newLiveKubernetes(ctx, rec)
	if err != nil {
		return RealWatch{}, err
	}
	grace := int64(1)
	pod, err := cluster.Clientset.CoreV1().Pods(cluster.Namespace).Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			SchedulingGates: []corev1.PodSchedulingGate{{Name: startupSchedulingGate}},
			Containers: []corev1.Container{{Name: "main", Image: "busybox:1.37.0",
				Command: []string{"sh", "-ec", "while [ ! -f /tmp/release ]; do sleep 0.1; done; printf 'watch-burst-finished\\n'"},
			}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return RealWatch{}, err
	}
	if pod.UID == "" || pod.ResourceVersion == "" || pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName != "" {
		return RealWatch{}, fmt.Errorf("burst pod lacks an actual unscheduled Pending identity")
	}
	fmt.Printf("actual gated burst pod %s/%s UID %s RV %s\n", cluster.Namespace, name, pod.UID, pod.ResourceVersion)
	return (RealWatch{Clientset: cluster.Clientset, Namespace: cluster.Namespace, Name: name,
		Ctx: ctx, config: cluster.Config}).withWatchRoute(rec)
}

// startLiveWatchedPod preserves the initial read before an actual kubelet transition.
func (w RealWatch) startLiveWatchedPod() (*corev1.Pod, error) {
	initial := w.Observed
	if w.Err != nil || initial == nil || initial.Name != w.Name || initial.UID == "" ||
		initial.ResourceVersion == "" || initial.Status.Phase != corev1.PodPending || initial.Spec.NodeName != "" {
		return nil, fmt.Errorf("live watch requires the runtime's initial gated Pending read")
	}
	if err := w.releaseSchedulingGate(initial); err != nil {
		return nil, err
	}
	running, err := awaitLivePod(w.Ctx, liveKubernetes{Clientset: w.Clientset, Namespace: w.Namespace}, w.Name)
	if err != nil {
		return nil, err
	}
	if running.UID != initial.UID || running.ResourceVersion == initial.ResourceVersion ||
		len(running.Status.ContainerStatuses) != 1 || running.Status.ContainerStatuses[0].RestartCount != 0 {
		return nil, fmt.Errorf("live Running observation did not advance the original pod without restart")
	}
	return running, nil
}

// completeLiveWatchedPod uses the direct admin transport even while the runtime watch route is closed.
func (w RealWatch) completeLiveWatchedPod(running *corev1.Pod) (*corev1.Pod, error) {
	if w.directConfig == nil {
		return nil, fmt.Errorf("live completion requires the direct admin connection")
	}
	containerID := running.Status.ContainerStatuses[0].ContainerID
	executor := jetbridge.NewSPDYExecutor(w.Clientset, w.directConfig)
	if err := executor.ExecInPod(w.Ctx, w.Namespace, w.Name, "main",
		[]string{"sh", "-ec", "touch /tmp/release"}, nil, nil, nil, false,
		jetbridge.ExecAttrs{Purpose: "release-watch-burst"}); err != nil {
		return nil, err
	}
	completed, _, err := awaitLivePodExit(w.Ctx, w.Clientset, running, 0, "watch-burst-finished\n")
	if err != nil {
		return nil, err
	}
	if completed.Status.ContainerStatuses[0].ContainerID != containerID {
		return nil, fmt.Errorf("burst completion changed the Running container identity")
	}
	return completed, nil
}

func (w RealWatch) observeLiveBurst(first, second string) (RealWatch, error) {
	if first != string(corev1.PodRunning) || second != string(corev1.PodSucceeded) {
		return w, fmt.Errorf("real burst requires Running then Succeeded, got %q then %q", first, second)
	}
	running, err := w.startLiveWatchedPod()
	if err != nil {
		return w, err
	}
	containerID := running.Status.ContainerStatuses[0].ContainerID
	fmt.Printf("actual burst Running %s/%s UID %s RV %s node %s container %s before completion release\n",
		w.Namespace, w.Name, running.UID, running.ResourceVersion, running.Spec.NodeName, containerID)
	completed, err := w.completeLiveWatchedPod(running)
	if err != nil {
		return w, err
	}
	fmt.Printf("actual burst Succeeded %s/%s UID %s RV %s node %s container %s exit 0 before runtime drain\n",
		w.Namespace, w.Name, completed.UID, completed.ResourceVersion, completed.Spec.NodeName, containerID)

	// Preserve the original consumer: drain queued updates to the final phase.
	// Both real lifecycle transitions above completed before this next read.
	out := w.next()
	for out.Err == nil && out.Observed != nil && string(out.Observed.Status.Phase) != second {
		out = out.next()
	}
	return out, nil
}
