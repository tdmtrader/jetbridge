package steps

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/worker/jetbridge"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
)

const startupSchedulingGate = "brine.test/observe-startup"

// A scheduling gate keeps kubelet startup behind the runtime's real watch.
// PullAlways requires a registry pull cycle; cached layers need not download.
func prepareLiveImagePull(database JetbridgeDB, capture SpanCapture, rec *brine.Recorder) (ExecStepRunning, error) {
	w, err := newLiveRuntimeWorker(database, rec)
	if err != nil {
		return ExecStepRunning{}, err
	}
	const handle = "observed-pull"
	name := jetbridge.GeneratePodName(db.ContainerMetadata{Type: db.ContainerTypeTask}, handle)
	grace := int64(1)
	pod, err := w.Clientset.CoreV1().Pods(w.Namespace).Create(w.Ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, TerminationGracePeriodSeconds: &grace,
			SchedulingGates: []corev1.PodSchedulingGate{{Name: startupSchedulingGate}},
			Containers:      []corev1.Container{{Name: "main", Image: "busybox:1.37.0", ImagePullPolicy: corev1.PullAlways, Command: []string{"sleep", "600"}}},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return ExecStepRunning{}, err
	}
	for {
		pod, err = w.Clientset.CoreV1().Pods(w.Namespace).Get(w.Ctx, name, metav1.GetOptions{})
		if err != nil {
			return ExecStepRunning{}, err
		}
		gated := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == "SchedulingGated" {
				gated = true
			}
		}
		if gated && pod.Status.Phase == corev1.PodPending && pod.Spec.NodeName == "" && pod.UID != "" && len(pod.Status.ContainerStatuses) == 0 {
			break
		}
		select {
		case <-w.Ctx.Done():
			return ExecStepRunning{}, fmt.Errorf("pod never reached its actual scheduling gate: %w", w.Ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	fmt.Printf("actual scheduling-gated pod %s/%s UID %s RV %s has no node or container status\n", w.Namespace, name, pod.UID, pod.ResourceVersion)
	return newLiveStartupProcess(w, capture, rec, pod, handle, "")
}

func (in ExecStepRunning) completeLiveImagePull() (SpansRecorded, error) {
	l := in.live
	if l == nil || len(l.before.Spec.SchedulingGates) != 1 || l.before.Spec.SchedulingGates[0].Name != startupSchedulingGate {
		return SpansRecorded{}, fmt.Errorf("image observation requires an actual scheduling-gated pod")
	}
	ctx, cancel := context.WithTimeout(in.Ctx, 45*time.Second)
	defer cancel()
	// This is an independent, unmodified Kubernetes watch. It proves the
	// physical transitions without relying on the SUT's trace events.
	stream, err := l.observer.CoreV1().Pods(in.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + in.Handle, ResourceVersion: l.before.ResourceVersion,
	})
	if err != nil {
		return SpansRecorded{}, err
	}
	defer stream.Stop()
	creatingRV := ""
	out, err := runLiveStartup(in, func(runCtx context.Context) error {
		pod := l.before.DeepCopy()
		pod.Spec.SchedulingGates = nil
		updated, err := l.observer.CoreV1().Pods(in.Namespace).Update(runCtx, pod, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		if updated.UID != l.before.UID {
			return fmt.Errorf("scheduling gate update changed pod identity")
		}
		fmt.Printf("released scheduling gate for pod UID %s only after the runtime watch handshake\n", updated.UID)
		for {
			select {
			case event, ok := <-stream.ResultChan():
				if !ok {
					return fmt.Errorf("independent image-startup watch closed: %v", ctx.Err())
				}
				if event.Type == watch.Error {
					return apierrors.FromObject(event.Object)
				}
				pod, ok := event.Object.(*corev1.Pod)
				if !ok || pod.UID != l.before.UID || event.Type == watch.Deleted {
					return fmt.Errorf("independent startup watch lost the original pod: %s %T", event.Type, event.Object)
				}
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.Name == "main" && cs.State.Waiting != nil && cs.State.Waiting.Reason == "ContainerCreating" {
						creatingRV = pod.ResourceVersion
					}
				}
				if pod.Status.Phase == corev1.PodRunning {
					if creatingRV == "" || creatingRV == pod.ResourceVersion || pod.Spec.NodeName == "" || len(pod.Status.ContainerStatuses) != 1 {
						return fmt.Errorf("actual pod did not transition from ContainerCreating to Running")
					}
					cs := pod.Status.ContainerStatuses[0]
					if cs.Name != "main" || cs.State.Running == nil || cs.ImageID == "" || pod.Spec.Containers[0].ImagePullPolicy != corev1.PullAlways {
						return fmt.Errorf("actual pulled image is not running")
					}
					l.after = pod
					fmt.Printf("independent watch saw pod UID %s ContainerCreating RV %s -> Running RV %s on node %s with image ID %s\n", pod.UID, creatingRV, pod.ResourceVersion, pod.Spec.NodeName, cs.ImageID)
					return nil
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	if err != nil {
		return out, err
	}
	if out.WaitErr != nil {
		return out, fmt.Errorf("real pull startup failed before trace assertions: %w", out.WaitErr)
	}
	if l.stdout.String() != "startup-observed" {
		return out, fmt.Errorf("actual pulled-image task output missing: %q", l.stdout.String())
	}
	// The trace labels ContainerCreating as pulling, although that status can
	// also include other setup. Require kubelet pull events for this case,
	// rather than treating the status label alone as proof of image pulling.
	for {
		events, err := l.observer.CoreV1().Events(in.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.uid=" + string(l.before.UID)})
		if err != nil {
			return out, err
		}
		reasons := map[string]bool{}
		for _, event := range events.Items {
			if event.InvolvedObject.UID != l.before.UID || event.InvolvedObject.FieldPath != "spec.containers{main}" || event.Type != corev1.EventTypeNormal ||
				(event.Source.Component != "kubelet" && event.ReportingController != "kubelet" && event.ReportingController != "kubernetes.io/kubelet") ||
				!strings.Contains(event.Message, "busybox:1.37.0") {
				continue
			}
			if event.Reason == "Pulling" || event.Reason == "Pulled" {
				reasons[event.Reason] = true
			}
		}
		if reasons["Pulling"] && reasons["Pulled"] {
			fmt.Printf("actual kubelet Pulling and Pulled events identify pod UID %s main image busybox:1.37.0; task stdout %q\n", l.before.UID, l.stdout.String())
			out.live = l
			return out, nil
		}
		select {
		case <-ctx.Done():
			return out, fmt.Errorf("no actual kubelet Pulling/Pulled pair for the pod: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
