package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// recreatePausePod replaces a pause pod that died before the step's process
// could start. Both paths that can find the pod dead call it — waitForRunning,
// when the pod goes terminal before it ever reached Running, and the exec
// retry loop, when the SPDY dial cannot connect — so that they cannot come to
// different conclusions about the same pod.
//
// The phase is not the signal. A pause pod the GC reaper collects or a node
// drain stops exits 0 and is Succeeded; an evicted or preempted one is Failed
// with a Reason; an OOM of the pause process is Failed as well. In every one
// of those the step has run nothing yet, so replacing the pod costs a pod
// creation and refusing costs the user a failed build. It is what has happened
// to the STEP, not what phase the pod is in, that decides: the pod is replaced
// exactly once, and never once the exec transport has carried a byte, because
// a second exec would run the command's side effects a second time.
//
// pod may be nil, in which case the current pod is fetched. It returns an
// error describing why no replacement was made; callers report that and fall
// back to their own diagnostics.
func (p *execProcess) recreatePausePod(ctx context.Context, pod *corev1.Pod) error {
	if p.container == nil {
		return fmt.Errorf("no container reference to recreate pause pod %s", p.podName)
	}
	if p.execTransportLive.Load() {
		return fmt.Errorf("pause pod %s died after the step's command started — not re-running it", p.podName)
	}
	if p.pausePodRecreated {
		return fmt.Errorf("pause pod %s has already been replaced once", p.podName)
	}
	// A new Pod is a new Pod UID, and a capture-held incarnation may not give
	// one a write-capable mount. The ordinary recreation is unchanged
	// -- it is the regression this refusal must not become -- so the question
	// is asked of the ledger rather than answered from a flag.
	if err := p.container.refuseIfCaptureHeld(ctx, "recreating the pause pod"); err != nil {
		return err
	}

	if pod == nil {
		fetched, err := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("get pause pod %s: %w", p.podName, err)
		}
		pod = fetched // nil when the pod is already gone
	}

	// A pod whose own init container failed did not die — it never came up.
	// Its inputs could not be staged, and a replacement would fail the same
	// way while replacing the only explanation the user has with a startup
	// timeout. Leave it for the caller's diagnostics.
	if name, exitCode, failed := failedInitContainer(pod); failed {
		return fmt.Errorf("pause pod %s did not die, its init container %q exited %d", p.podName, name, exitCode)
	}

	logger := lagerctx.FromContext(ctx).Session("recreate-pause-pod")
	phase, reason := "NotFound", ""
	if pod != nil {
		phase = string(pod.Status.Phase)
		reason = pod.Status.Reason
	}
	logger.Info("replacing-dead-pause-pod", lager.Data{
		"pod":    p.podName,
		"phase":  phase,
		"reason": reason,
	})

	// The replacement destroys the evidence, so write the post-mortem first.
	// Only for a pod that FAILED: a Succeeded pause pod is the reaper doing
	// its job, and diagnostics for it would be noise in every build log.
	if pod != nil && pod.Status.Phase == corev1.PodFailed {
		writePodDiagnostics(pod, p.processIO.Stderr)
		writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
	}

	// Spent whether or not the creation succeeds: one replacement per step.
	p.pausePodRecreated = true

	if pod != nil {
		_ = p.clientset.CoreV1().Pods(p.config.Namespace).Delete(ctx, p.podName, metav1.DeleteOptions{})
		// Wait for the old pod to be fully removed before creating a new one.
		for i := 0; i < 30; i++ {
			_, getErr := p.clientset.CoreV1().Pods(p.config.Namespace).Get(ctx, p.podName, metav1.GetOptions{})
			if getErr != nil {
				break // pod is gone
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	created, createErr := p.container.createPausePod(ctx, p.processSpec)
	if createErr != nil {
		return fmt.Errorf("recreate pause pod %s: %w", p.podName, createErr)
	}
	p.recreatedPodUID = executioncontrol.PodUID(created.UID)
	return nil
}

// waitForRunning bounds scheduling, startup and queued managed inputs with the
// same readiness budget that protects those inputs' pre-admitted readers' claims.
func (p *execProcess) waitForRunning(ctx context.Context) error {
	timeout := p.podReadyTimeout()
	startTime := time.Now()
	schedTimeout := podSchedulingTimeout(p.config)
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	watcher := NewPodWatcher(p.clientset, p.config.Namespace, p.podName)
	defer watcher.Stop()

	var lastPod *corev1.Pod
	var lastPhase corev1.PodPhase
	var unschedulableFirstSeen time.Time
	var unschedulableNotified bool
	countsSet := false
	tracker := newPodEventTracker()
	for {
		// An Unschedulable pod is updated once and then left alone, so a move
		// that falls due must not wait for an event that may never come: the
		// wait is bounded by what is left of the grace, and when it runs out
		// the last pod seen is looked at again.
		nextCtx, cancelNext := timeoutCtx, context.CancelFunc(func() {})
		if !unschedulableFirstSeen.IsZero() && lastPod != nil && p.canRelocate(lastPod) {
			nextCtx, cancelNext = context.WithTimeout(timeoutCtx, max(relocationGrace-time.Since(unschedulableFirstSeen), 0)+10*time.Millisecond)
		}
		pod, err := watcher.Next(nextCtx)
		cancelNext()
		if err != nil && nextCtx != timeoutCtx && timeoutCtx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			pod, err = lastPod, nil
		}
		if err != nil {
			if errors.Is(err, ErrPodDeleted) && pod != nil {
				writePodDiagnostics(pod, p.processIO.Stderr)
				writeNodeDiagnostics(ctx, p.clientset, pod, p.processIO.Stderr)
				return fmt.Errorf("pod deleted externally before reaching Running: %s", pod.Status.Phase)
			}
			// Check if this was a timeout vs other error.
			if timeoutCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				// If we were waiting for scheduling, report a scheduling
				// timeout rather than the generic startup timeout.
				if !unschedulableFirstSeen.IsZero() && lastPod != nil {
					msg, _ := isPodUnschedulable(lastPod)
					writePodDiagnostics(lastPod, p.processIO.Stderr)
					return fmt.Errorf("pod scheduling timeout: Unschedulable for %s: %s", schedTimeout, msg)
				}
				if lastPod != nil {
					writePodDiagnostics(lastPod, p.processIO.Stderr)
					return fmt.Errorf("timed out waiting for pod to start (timeout: %s, phase: %s)", timeout, lastPod.Status.Phase)
				}
				return fmt.Errorf("timed out waiting for pod to start (timeout: %s)", timeout)
			}
			return err
		}
		lastPod = pod

		// Set container count span attributes from pod spec on first event.
		if !countsSet {
			span := oteltrace.SpanFromContext(ctx)
			span.SetAttributes(
				attribute.Int64("init.container.count", int64(len(pod.Spec.InitContainers))),
				attribute.Int64("container.count", int64(len(pod.Spec.Containers))),
			)
			countsSet = true
		}

		if pod.Status.Phase != lastPhase {
			lastPhase = pod.Status.Phase
			oteltrace.SpanFromContext(ctx).AddEvent(
				"pod.phase."+strings.ToLower(string(pod.Status.Phase)),
				oteltrace.WithAttributes(attribute.String("pod.phase", string(pod.Status.Phase))),
			)
		}

		tracker.emitPodLifecycleEvents(ctx, pod)

		// The pause pod reached a terminal phase without ever running the
		// step. Whatever took it — the reaper, a drain (Succeeded), an
		// eviction or preemption (Failed, with a Reason), an OOM of the
		// pause process (Failed) — replace it once and keep waiting. This
		// runs before the failure classification below so that a pod which
		// died before the step started is retried rather than reported;
		// once the one replacement is spent, the classification stands.
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			recreateErr := p.recreatePausePod(ctx, pod)
			if recreateErr == nil {
				// Restart the watcher on the fresh pod.
				watcher.Stop()
				watcher = NewPodWatcher(p.clientset, p.config.Namespace, p.podName)
				continue
			}
			lagerctx.FromContext(ctx).Session("wait-for-running").Info("not-recreating-pause-pod", lager.Data{
				"pod":    p.podName,
				"phase":  string(pod.Status.Phase),
				"reason": pod.Status.Reason,
				"why":    recreateErr.Error(),
			})
		}

		// Check for terminal failure states BEFORE checking Running phase,
		// because CrashLoopBackOff can occur while the pod phase is Running.
		// OOM first (pod_status.go) — more actionable than CrashLoopBackOff.
		state := podStateFor(pod)
		if err := reportPodFailure(ctx, p.clientset, pod, p.processIO.Stderr, state); err != nil {
			return err
		}
		if message, unschedulable := isPodUnschedulable(pod); unschedulable {
			if unschedulableFirstSeen.IsZero() {
				unschedulableFirstSeen = time.Now()
			}
			if !unschedulableNotified {
				fmt.Fprintf(p.processIO.Stderr, "pod is Unschedulable: %s — waiting up to %s for cluster resources\n", message, podSchedulingTimeout(p.config))
				unschedulableNotified = true
			}
			if time.Since(unschedulableFirstSeen) > relocationGrace && p.canRelocate(pod) {
				outcome, err := p.relocateUnboundPod(ctx, pod, message)
				if err != nil {
					return err
				}
				if outcome != podUntouched {
					// A new pod: watch it from the start, or the old one's
					// deletion reads as the step's pod being taken away.
					watcher.Stop()
					watcher = NewPodWatcher(p.clientset, p.config.Namespace, p.podName)
					if outcome == podMoved {
						// On another node, the scheduling wait starts over.
						unschedulableFirstSeen = time.Time{}
						unschedulableNotified = false
					}
					continue
				}
			}
			if time.Since(unschedulableFirstSeen) > schedTimeout {
				writePodDiagnostics(pod, p.processIO.Stderr)
				return fmt.Errorf("pod scheduling timeout: Unschedulable for %s: %s", schedTimeout, message)
			}
		} else {
			unschedulableFirstSeen = time.Time{}
			unschedulableNotified = false
		}

		if pod.Status.Phase == corev1.PodRunning {
			p.runningNodeName = pod.Spec.NodeName
			metric.Metrics.K8sPodStartupDuration.Set(time.Since(startTime).Milliseconds())
			return nil
		}

		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			// The replacement above was not available or not appropriate —
			// report the death, with init container diagnostics.
			var initStatuses []string
			for _, cs := range pod.Status.InitContainerStatuses {
				state := "unknown"
				if cs.State.Terminated != nil {
					state = fmt.Sprintf("exit=%d reason=%s", cs.State.Terminated.ExitCode, cs.State.Terminated.Reason)
					if cs.State.Terminated.ExitCode != 0 {
						logReq := p.clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{
							Container: cs.Name,
						})
						if logBytes, logErr := logReq.Do(ctx).Raw(); logErr == nil {
							state += fmt.Sprintf(" logs=%q", string(logBytes))
						}
					}
				} else if cs.State.Waiting != nil {
					state = fmt.Sprintf("waiting reason=%s", cs.State.Waiting.Reason)
				}
				initStatuses = append(initStatuses, fmt.Sprintf("%s(%s)", cs.Name, state))
			}
			return fmt.Errorf("pod terminated before exec could run (phase: %s, initContainers: [%s])",
				pod.Status.Phase, strings.Join(initStatuses, ", "))
		}
	}
}
