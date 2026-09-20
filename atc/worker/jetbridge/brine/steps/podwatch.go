package steps

import (
	"context"
	"fmt"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

// Expiry is supplied by the live API watch cache, not a Status fixture or
// shared-cluster compaction. Only owned pods and metadata are changed.
func PodWatchDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[brine.Empty, RealWatch](
			"a real pod {string} whose watch history can expire",
			func(_ brine.Empty, p brine.Params, rec *brine.Recorder) (RealWatch, error) {
				name, ok := p.GetString(0)
				if !ok {
					return RealWatch{}, fmt.Errorf("expected a pod name")
				}
				return newLiveGatedWatchWithTimeout(rec, name, 6*time.Minute)
			},
		),
		brine.DefineMap[RealWatch, RealWatch](
			"the pod finishes before its watched history expires",
			func(in RealWatch, _ brine.Params, _ *brine.Recorder) (RealWatch, error) {
				return in.expireHistoryAndRead()
			},
		),
	}
}

func (w RealWatch) expireHistoryAndRead() (RealWatch, error) {
	if w.Observed == nil || w.Err != nil {
		return w, fmt.Errorf("expiry requires a successful initial pod read: %v", w.Err)
	}
	initial := w.Observed.DeepCopy()
	running, err := w.startLiveWatchedPod()
	if err != nil {
		return w, err
	}
	completed, err := w.completeLiveWatchedPod(running)
	if err != nil {
		return w, err
	}
	if completed.UID != initial.UID || initial.Status.Phase != corev1.PodPending || completed.Status.Phase != corev1.PodSucceeded {
		return w, fmt.Errorf("expiry requires actual Pending-to-Succeeded history for the same pod")
	}
	fmt.Printf("actual expiry lifecycle: pod %s/%s UID %s Pending RV %s -> Running RV %s -> Succeeded RV %s\n",
		w.Namespace, w.Name, initial.UID, initial.ResourceVersion, running.ResourceVersion, completed.ResourceVersion)
	ctx, cancel := context.WithTimeout(w.Ctx, 5*time.Minute)
	defer cancel()
	if err := w.awaitExpiredHistory(ctx, initial); err != nil {
		return w, err
	}
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	current, err := pods.Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return w, fmt.Errorf("read completed pod after history expiry: %w", err)
	}
	if current.UID != initial.UID || current.Status.Phase != corev1.PodSucceeded {
		return w, fmt.Errorf("completed pod changed identity or phase while history expired")
	}
	// The existing contract resumes from a version newer than the expired
	// history. The kubelet has finished; advance metadata only so the first
	// fallback reads a fresh version while retaining its actual terminal phase.
	previousVersion := current.ResourceVersion
	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	current.Annotations["brine.jetbridge/expiry-checkpoint"] = initial.ResourceVersion
	current, err = pods.Update(ctx, current, metav1.UpdateOptions{})
	if err != nil {
		return w, fmt.Errorf("publish fresh completed-pod checkpoint: %w", err)
	}
	if current.UID != initial.UID || current.Status.Phase != corev1.PodSucceeded || current.ResourceVersion == previousVersion {
		return w, fmt.Errorf("fresh completed-pod checkpoint lost identity, phase or version advancement")
	}
	fmt.Printf("fresh expiry checkpoint: pod %s/%s UID %s RV %s phase %s\n", w.Namespace, w.Name, current.UID, current.ResourceVersion, current.Status.Phase)
	readCtx, stopRead := context.WithTimeout(ctx, 3*time.Second)
	defer stopRead()
	w = w.readAfterExpiry(readCtx)
	// A timeout is not evidence of recovery. Keep it for the shared phase
	// assertion, after proving the API is healthy and still has the same pod.
	healthy, err := pods.Get(ctx, w.Name, metav1.GetOptions{})
	if err != nil {
		return w, fmt.Errorf("verify API health after expiry: %w", err)
	}
	if healthy.UID != initial.UID || healthy.Status.Phase != corev1.PodSucceeded || healthy.ResourceVersion != current.ResourceVersion {
		return w, fmt.Errorf("fresh API read differs from the completed pod")
	}
	if w.Err == nil && (w.Observed == nil || w.Observed.UID != healthy.UID || w.Observed.ResourceVersion != healthy.ResourceVersion || w.Observed.Status.Phase != healthy.Status.Phase) {
		w.Err = fmt.Errorf("expiry recovery did not return the current pod identity/version/phase")
	}
	if w.Err != nil {
		return w, nil
	}
	// Publish another version, then delete the pod before the next read.
	// Only a watch resumed from the refreshed version can replay this update;
	// another fallback Get (or a versionless watch) cannot find the pod.
	if healthy.Annotations == nil {
		healthy.Annotations = map[string]string{}
	}
	healthy.Annotations["brine.jetbridge/after-expiry"] = healthy.ResourceVersion
	checkpoint, err := pods.Update(ctx, healthy, metav1.UpdateOptions{})
	if err != nil {
		return w, fmt.Errorf("publish post-expiry checkpoint: %w", err)
	}
	zero := int64(0)
	if err := pods.Delete(ctx, w.Name, metav1.DeleteOptions{
		GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &checkpoint.UID},
	}); err != nil {
		return w, fmt.Errorf("delete post-expiry pod: %w", err)
	}
	if _, err := pods.Get(ctx, w.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return w, fmt.Errorf("post-expiry pod must be gone before resuming, got %v", err)
	}
	w = w.readAfterExpiry(readCtx)
	if w.Err == nil && (w.Observed == nil || w.Observed.UID != checkpoint.UID || w.Observed.ResourceVersion != checkpoint.ResourceVersion) {
		w.Err = fmt.Errorf("post-expiry watch did not replay the exact checkpoint")
	}
	return w, nil
}

// Turn a production panic into a named scenario failure, not a dead adapter.
func (w RealWatch) readAfterExpiry(ctx context.Context) (out RealWatch) {
	out = w
	defer func() {
		if r := recover(); r != nil {
			out.Observed = nil
			out.Err = fmt.Errorf("the runtime panicked while reading an expired watch: %v", r)
		}
	}()
	out.Observed, out.Err = w.Watcher.Next(ctx)
	return out
}

// awaitExpiredHistory advances only an owned, scheduling-gated pod's metadata.
// The real cache decides when the initial version expires. Probing is independent
// of PodWatcher; a real terminal Expired/410 event and closure are required.
func (w RealWatch) awaitExpiredHistory(ctx context.Context, initial *corev1.Pod) error {
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	churn, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "watch-history-"},
		Spec: corev1.PodSpec{
			SchedulingGates: []corev1.PodSchedulingGate{{Name: startupSchedulingGate}},
			RestartPolicy:   corev1.RestartPolicyNever,
			Containers:      []corev1.Container{{Name: "main", Image: "busybox:1.37.0", Command: []string{"true"}}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create owned history pod: %w", err)
	}
	if churn.UID == "" || churn.Status.Phase != corev1.PodPending || churn.Spec.NodeName != "" {
		return fmt.Errorf("history pod is not an API-owned, gated Pending pod")
	}
	churnUID := churn.UID
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	for updates := 0; ; updates++ {
		if updates%20 == 0 {
			expired, err := w.historyExpired(ctx, initial.ResourceVersion)
			if err != nil {
				return err
			}
			if expired {
				fmt.Printf("actual expired watch: pod %s/%s UID %s RV %s; terminal Expired/410 and stream closure after %d owned metadata updates (%s)\n",
					w.Namespace, w.Name, initial.UID, initial.ResourceVersion, updates, time.Since(start).Round(time.Millisecond))
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned history did not expire after %d updates: %w", updates, ctx.Err())
		case <-ticker.C:
		}
		if churn.Annotations == nil {
			churn.Annotations = map[string]string{}
		}
		churn.Annotations["brine.jetbridge/history-sequence"] = fmt.Sprint(updates)
		updated, err := pods.Update(ctx, churn, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("advance owned pod metadata: %w", err)
		}
		if updated.UID != churnUID || updated.Spec.NodeName != "" || updated.Status.Phase != corev1.PodPending || updated.ResourceVersion == churn.ResourceVersion {
			return fmt.Errorf("history update lost owned identity, scheduling gate or version advancement")
		}
		churn = updated
	}
}

func (w RealWatch) historyExpired(ctx context.Context, version string) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stream, err := jetbridge.WatchPod(probeCtx, w.Clientset, w.Namespace, w.Name, version)
	if err != nil {
		return false, fmt.Errorf("open history probe: %w", err)
	}
	defer stream.Stop()
	select {
	case event, open := <-stream.ResultChan():
		if !open {
			return false, fmt.Errorf("history probe closed without an event")
		}
		if event.Type != watch.Error {
			pod, ok := event.Object.(*corev1.Pod)
			if !ok || pod.UID == "" || pod.Name != w.Name || pod.Namespace != w.Namespace {
				return false, fmt.Errorf("history probe returned an unrelated object: %#v", event)
			}
			return false, nil
		}
		status, ok := event.Object.(*metav1.Status)
		if !ok || status.Code != 410 || status.Reason != metav1.StatusReasonExpired {
			return false, fmt.Errorf("history probe returned an unexpected error: %#v", event)
		}
	case <-probeCtx.Done():
		return false, fmt.Errorf("history probe returned no event: %w", probeCtx.Err())
	}
	select {
	case event, open := <-stream.ResultChan():
		if open {
			return false, fmt.Errorf("expired history stream did not close: %#v", event)
		}
		return true, nil
	case <-probeCtx.Done():
		return false, fmt.Errorf("expired history stream did not close: %w", probeCtx.Err())
	}
}
