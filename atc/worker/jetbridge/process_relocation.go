package jetbridge

import (
	"context"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// relocationGrace is how long a pod pinned to its execution's node may sit
// Unschedulable before the execution moves to another node. Long enough that a
// node draining one finished step is not abandoned for it, short beside the
// scheduling timeout the pod would otherwise wait out. A variable so tests can
// shorten it.
var relocationGrace = 2 * time.Minute

type relocation int

const (
	podUntouched relocation = iota
	podReplacedInPlace
	podMoved
)

// canRelocate reports whether this step's pod may be moved: it belongs to a
// Run execution pinned to a node, the Run can move it, it has never been bound
// to a node, nothing has been exec'd into it, and no earlier move was refused.
func (p *execProcess) canRelocate(pod *corev1.Pod) bool {
	return p.container != nil && p.container.relocate != nil &&
		p.control != nil && p.control.Node != nil &&
		pod.Spec.NodeName == "" && !p.execTransportLive.Load() && !p.relocationSpent &&
		(pod.ResourceVersion == "" || pod.ResourceVersion != p.relocationTried)
}

// relocateUnboundPod moves a Run execution whose pod could not be scheduled on
// the node it was admitted on to another node, and creates the pod again there.
//
// The proof that the step never ran is the delete: it carries a precondition
// on the pod's UID and resourceVersion as they were when the pod was seen
// unbound, and binding a pod changes its resourceVersion. A delete that
// succeeds removed a pod no kubelet was ever given, so no process was ever
// started in it; one that is refused means the pod changed -- it may just have
// been bound -- and the step stays where it is.
//
// It reports what became of the pod: untouched, replaced on the node it was
// on (the move was refused), or moved. An error means the step is left with no
// pod and cannot go on.
func (p *execProcess) relocateUnboundPod(ctx context.Context, pod *corev1.Pod, reason string) (relocation, error) {
	from := p.control.Node.Name
	logger := lagerctx.FromContext(ctx).Session("relocate-unbound-pod", lager.Data{
		"pod": p.podName, "execution": string(p.control.Identity.ExecutionID), "from": from,
	})

	// The admission attempt is waiting for this pod to be bound. It reads the
	// execution's control when one is, so it is stopped before the control is
	// replaced, and an attempt that already admitted means the pod was bound
	// after all.
	admitting := p.admission != nil
	if admitted := p.stopAdmission(); admitted {
		logger.Info("not-relocating-admitted-pod")
		p.relocationSpent = true
		return podUntouched, nil
	}
	resume := func() {
		if admitting {
			p.startAdmission(ctx)
		}
	}

	uid, version := pod.UID, pod.ResourceVersion
	grace := int64(0)
	err := p.clientset.CoreV1().Pods(p.config.Namespace).Delete(ctx, p.podName, metav1.DeleteOptions{
		Preconditions:      &metav1.Preconditions{UID: &uid, ResourceVersion: &version},
		GracePeriodSeconds: &grace,
	})
	if err != nil {
		// Changed (perhaps bound) or already gone: either way this is not the
		// pod that was seen unbound, and the ordinary path deals with it.
		logger.Info("not-relocating-changed-pod", lager.Data{"error": err.Error()})
		p.relocationTried = pod.ResourceVersion
		resume()
		return podUntouched, nil
	}
	if err := p.awaitPodGone(ctx); err != nil {
		return podUntouched, err
	}

	spec, err := p.container.relocate(ctx, p.relocatedFrom)
	if err != nil {
		// Still admitted where it was: put the pod back and let it wait there.
		logger.Info("relocation-refused", lager.Data{"error": err.Error()})
		p.relocationSpent = true
		if _, createErr := p.container.createPausePod(ctx, p.processSpec); createErr != nil {
			return podUntouched, fmt.Errorf("recreate pause pod %s after a refused relocation: %w", p.podName, createErr)
		}
		resume()
		return podReplacedInPlace, nil
	}

	p.relocatedFrom = append(p.relocatedFrom, from)
	p.container.containerSpec = spec
	p.control = spec.ExecutionControl
	if _, err := p.container.createPausePod(ctx, p.processSpec); err != nil {
		return podUntouched, fmt.Errorf("create pause pod %s on node %s: %w", p.podName, p.control.Node.Name, err)
	}
	resume()
	logger.Info("relocated", lager.Data{"to": p.control.Node.Name})
	fmt.Fprintf(p.processIO.Stderr, "pod could not be scheduled on node %s (%s): moved the step to node %s\n",
		from, reason, p.control.Node.Name)
	return podMoved, nil
}

// awaitPodGone waits for the deleted pod's name to be free again. An unbound
// pod has no kubelet to finish it, so this is the API server's own latency.
func (p *execProcess) awaitPodGone(ctx context.Context) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(30 * time.Second)
	for {
		_, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("pod %s was deleted for relocation and is still present after 30s", p.podName)
		case <-ticker.C:
		}
	}
}
