package jetbridge

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// InterruptExecution requests interruption of the original journaled command:
// a supervised task, or a Run-owned resource command. The persistent
// supervisor, or the resource session's cancel marker and group kill, takes
// effect after this transport returns. Only the command's later exit journal
// can establish an outcome; requesting a stop is not one. A start whose
// delivery never claimed the journal is closed in the Pod as stopped
// (exactStopScript), so the execution it names has an outcome writer.
func (s *OutputSource) InterruptExecution(ctx context.Context, node string, start executioncontrol.Acknowledgement) error {
	if err := start.Validate(); err != nil {
		return err
	}
	if start.Kind != executioncontrol.AcknowledgementStart {
		return output.ErrInvalidIdentity
	}
	state, resource, err := executionJournalFromIdentity(start.ProcessIdentity)
	if err != nil {
		return fmt.Errorf("%w: %v", output.ErrUnresolved, err)
	}
	client, err := s.recoveryClient(ctx, node, string(start.NodeUID), start.ActivationEpoch)
	if err != nil {
		return err
	}
	classified, err := client.Classify(ctx, start.Identity)
	if err != nil {
		return err
	}
	if err := classified.Validate(); err != nil {
		return err
	}
	if classified.Identity != start.Identity {
		return output.ErrInvalidIdentity
	}
	if classified.Classification != executioncontrol.ClassificationExecuting {
		return nil
	}
	original, err := client.RecordStart(ctx, start.Identity, start.PodUID, start.ProcessIdentity)
	if err != nil {
		return err
	}
	if original != start {
		return output.ErrInvalidIdentity
	}
	stop, err := client.RequestStop(ctx, start.Identity)
	if err != nil {
		return err
	}
	if !stop.Accepted {
		return output.ErrUnresolved
	}
	pods, err := s.client.CoreV1().Pods(s.controls.config.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if string(pod.UID) != string(start.PodUID) {
			continue
		}
		if resource {
			return requestResourceStop(ctx, s.client, s.executor, pod.Namespace, pod.Name, node, state, start)
		}
		return requestSupervisorStop(ctx, s.client, s.executor, pod.Namespace, pod.Name, node, state, start)
	}
	return fmt.Errorf("%w: original supervisor Pod is absent", output.ErrUnresolved)
}

func requestSupervisorStop(ctx context.Context, client kubernetes.Interface, executor PodExecutor, namespace, podName, nodeName, state string, start executioncontrol.Acknowledgement) error {
	if executor == nil {
		return output.ErrIncomplete
	}
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return err
	}
	if err := executor.ExecInPod(ctx, namespace, podName, mainContainerName, exactTaskStopCommand(state), nil, nil, nil, false, ExecAttrs{Purpose: "exact-stop-request"}); err != nil {
		return err
	}
	return checkSupervisorPod(ctx, client, namespace, podName, nodeName, start)
}

// requestResourceStop runs the resource cancel script against the original
// Pod: it leaves the cancellation marker, so a delivery that has not reached
// the command yet exits 130, and kills the command's own process group if its
// PID record still names the same process lifetime. The marker is written
// before the PID is read, the reverse of the command's own order, so one of
// the two always sees the other. The session outside that group journals the
// exit; this writes one only for a start no delivery has claimed.
func requestResourceStop(ctx context.Context, client kubernetes.Interface, executor PodExecutor, namespace, podName, nodeName, state string, start executioncontrol.Acknowledgement) error {
	if executor == nil {
		return output.ErrIncomplete
	}
	if err := checkSupervisorPod(ctx, client, namespace, podName, nodeName, start); err != nil {
		return err
	}
	var stderr strings.Builder
	if err := executor.ExecInPod(ctx, namespace, podName, mainContainerName,
		exactResourceStopCommand(state),
		nil, io.Discard, &stderr, false, ExecAttrs{Purpose: "cancel-resource"}); err != nil {
		return fmt.Errorf("stop resource command: %w: %s", err, stderr.String())
	}
	return checkSupervisorPod(ctx, client, namespace, podName, nodeName, start)
}
