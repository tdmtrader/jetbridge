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

// Observe actual failed attempts on one logical init, followed by recovery.
// OnFailure is explicitly a pre-existing fixture policy, not the production
// pod default. The kubelet owns all status; no reported lifecycle is injected.
func prepareLiveRetryInit(database JetbridgeDB, capture SpanCapture, rec *brine.Recorder) (ExecStepRunning, error) {
	w, err := newLiveRuntimeWorker(database, rec, 1)
	if err != nil {
		return ExecStepRunning{}, err
	}
	const handle = "observed-init-retry"
	const initName = "artifact-init"
	grace := int64(1)
	command := "n=0; [ ! -f /state/attempt ] || n=$(cat /state/attempt); n=$((n+1)); printf '%s' \"$n\" > /state/attempt; printf 'attempt=%s\\n' \"$n\"; if [ \"$n\" -eq 1 ]; then touch /state/gate-ready; while [ ! -f /state/start ]; do sleep 0.1; done; fi; if [ \"$n\" -gt 2 ]; then while [ ! -f /state/ready-data ]; do sleep 0.1; done; fi; cat /state/ready-data"
	pods := w.Clientset.CoreV1().Pods(w.Namespace)
	_, err = pods.Create(w.Ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: handle}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyOnFailure, TerminationGracePeriodSeconds: &grace,
		Volumes:        []corev1.Volume{{Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		InitContainers: []corev1.Container{{Name: initName, Image: "busybox:1.37.0", Command: []string{"sh", "-ec", command}, VolumeMounts: []corev1.VolumeMount{{Name: "state", MountPath: "/state"}}}},
		Containers:     []corev1.Container{{Name: "main", Image: "busybox:1.37.0", Command: []string{"sleep", "600"}}},
	}}, metav1.CreateOptions{})
	if err != nil {
		return ExecStepRunning{}, err
	}
	for {
		pod, err := pods.Get(w.Ctx, handle, metav1.GetOptions{})
		if err != nil {
			return ExecStepRunning{}, err
		}
		if pod.UID != "" && pod.Spec.NodeName != "" && pod.Status.Phase == corev1.PodPending && len(pod.Status.InitContainerStatuses) == 1 && pod.Status.InitContainerStatuses[0].State.Running != nil {
			err := w.Executor.ExecInPod(w.Ctx, w.Namespace, handle, initName, []string{"test", "-f", "/state/gate-ready"}, nil, nil, nil, false, jetbridge.ExecAttrs{})
			if err == nil {
				fmt.Printf("real retry init staged on pod %s/%s UID %s node %s; OnFailure is explicit fixture policy, not production's default\n", w.Namespace, handle, pod.UID, pod.Spec.NodeName)
				return newLiveStartupProcess(w, capture, rec, pod, handle, initName)
			}
		}
		select {
		case <-w.Ctx.Done():
			return ExecStepRunning{}, w.Ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func recoverLiveRetryInit(in ExecStepRunning) (SpansRecorded, error) {
	if in.live == nil {
		return SpansRecorded{}, fmt.Errorf("no actual retry-init pod")
	}
	l := in.live
	pods := l.observer.CoreV1().Pods(in.Namespace)
	observed, err := pods.Watch(in.Ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + in.Handle, ResourceVersion: l.before.ResourceVersion})
	if err != nil {
		return SpansRecorded{}, err
	}
	defer observed.Stop()
	failures := map[string]bool{}
	out, err := runLiveStartup(in, func(ctx context.Context) error {
		if err := l.worker.Executor.ExecInPod(ctx, in.Namespace, in.Handle, l.initName, []string{"touch", "/state/start"}, nil, nil, nil, false, jetbridge.ExecAttrs{}); err != nil {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case event, ok := <-observed.ResultChan():
				if !ok {
					return fmt.Errorf("real init observer closed")
				}
				pod, ok := event.Object.(*corev1.Pod)
				if !ok {
					return fmt.Errorf("real init observer received %T", event.Object)
				}
				if pod.UID != l.before.UID {
					return fmt.Errorf("retry init pod was replaced")
				}
				for _, c := range pod.Status.InitContainerStatuses {
					if c.Name != l.initName {
						continue
					}
					if c.State.Terminated != nil {
						dead := c.State.Terminated
						fmt.Printf("actual retry init observation: pod UID %s RV %s container %s restart=%d exit=%d reason=%s phase=%s\n", pod.UID, pod.ResourceVersion, c.ContainerID, c.RestartCount, dead.ExitCode, dead.Reason, pod.Status.Phase)
						if dead.ExitCode == 1 && c.ContainerID != "" {
							failures[c.ContainerID] = true
						}
					}
					if c.State.Running != nil && c.RestartCount >= 2 {
						if len(failures) < 2 {
							return fmt.Errorf("real init restarted twice but only %d distinct current failures were observed", len(failures))
						}
						return l.worker.Executor.ExecInPod(ctx, in.Namespace, in.Handle, l.initName, []string{"sh", "-ec", "printf recovered > /state/ready-data"}, nil, nil, nil, false, jetbridge.ExecAttrs{})
					}
				}
			}
		}
	})
	if err != nil {
		return out, err
	}
	if out.WaitErr != nil || out.ExitStatus != 0 || l.stdout.String() != "startup-observed" {
		return out, fmt.Errorf("recovered init did not permit actual task execution: status=%d err=%v stdout=%q", out.ExitStatus, out.WaitErr, l.stdout.String())
	}
	final, err := pods.Get(in.Ctx, in.Handle, metav1.GetOptions{})
	if err != nil {
		return out, err
	}
	if final.UID != l.before.UID || final.Status.Phase != corev1.PodRunning || len(final.Status.InitContainerStatuses) != 1 {
		return out, fmt.Errorf("retry init lost final pod state")
	}
	c := final.Status.InitContainerStatuses[0]
	if c.State.Terminated == nil || c.State.Terminated.ExitCode != 0 || c.RestartCount != 2 {
		return out, fmt.Errorf("init did not recover after exactly two actual failures: %+v", c)
	}
	fmt.Printf("real init recovery: pod UID %s actual failures=%d restart=%d final init exit=0, task stdout=%q\n", final.UID, len(failures), c.RestartCount, l.stdout.String())
	return out, nil
}
