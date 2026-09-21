package jetbridge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// OutputDrain confirms the Kubernetes half of a source seal.
type OutputDrain struct {
	Client    kubernetes.Interface
	Controls  OutputControlResolver
	Namespace string
}

const outputDrainFinalizer = "jetbridge.concourse-ci.org/output-drain"

func (drain *OutputDrain) ConfirmDrain(ctx context.Context, node string, started output.SealStarted) ([]output.DrainedWriter, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := started.Validate(); err != nil {
		return nil, err
	}
	ack := started.Acknowledgement
	if drain.Client == nil || drain.Controls == nil || drain.Namespace == "" || ack.PodUID == "" {
		return nil, fmt.Errorf("%w: no exact pod or configured drain controller", output.ErrSealUnconfirmed)
	}
	control, err := drain.Controls.ForNode(ctx, node)
	if err != nil {
		return nil, err
	}
	pod, err := drain.podForUID(ctx, node, ack.PodUID)
	if err != nil {
		return nil, err
	}
	if pod == nil {
		return nil, fmt.Errorf("%w: the exact producing pod is missing", output.ErrSealUnconfirmed)
	}
	// Keep the final status readable across controller restarts and ambiguous
	// seal confirmations. Only acknowledged source release removes this pin.
	if !slices.Contains(pod.Finalizers, outputDrainFinalizer) {
		if pod.DeletionTimestamp != nil {
			return nil, fmt.Errorf("%w: deletion already started without a drain pin", output.ErrSealUnconfirmed)
		}
		pod.Finalizers = append(pod.Finalizers, outputDrainFinalizer)
		pod, err = drain.Client.CoreV1().Pods(drain.Namespace).Update(ctx, pod, metav1.UpdateOptions{})
		if err != nil {
			return nil, err
		}
	}
	// Deletion stops native sidecars and closes restart admission. A momentary
	// Terminated container status on a live pod is not a termination boundary.
	if pod.DeletionTimestamp == nil {
		err := drain.Client.CoreV1().Pods(drain.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion},
		})
		if err != nil {
			return nil, err
		}
		pod, err = drain.podForUID(ctx, node, ack.PodUID)
		if err != nil {
			return nil, err
		}
	}
	if pod == nil || pod.DeletionTimestamp == nil || !allPodContainersTerminated(pod) {
		return nil, fmt.Errorf("%w: waiting for every container in the exact pod to terminate", output.ErrSealUnconfirmed)
	}

	drained := make([]output.DrainedWriter, 0, len(started.DrainSet))
	for _, ticket := range started.DrainSet {
		inspection, err := control.InspectWriter(ctx, ack.Execution, ack.HandoffID, ticket)
		if err != nil {
			return nil, err
		}
		issued := inspection.Issued
		if err := issued.ValidateAs(output.CaptureWriterTicketIssued); err != nil {
			return nil, err
		}
		if issued.Execution != ack.Execution || issued.HandoffID != ack.HandoffID || issued.Incarnation != ack.Incarnation ||
			issued.ActivationEpoch != ack.ActivationEpoch || issued.WriterTicketID != ticket || issued.PodUID != ack.PodUID {
			return nil, fmt.Errorf("%w: writer lookup does not match the sealed source and exact pod", output.ErrInvalidIdentity)
		}
		closed, err := control.RetireWriter(ctx, output.WriterAdmission{
			ProtocolVersion: output.ProtocolVersion, Execution: issued.Execution, ActivationEpoch: issued.ActivationEpoch,
			HandoffID: issued.HandoffID, Incarnation: issued.Incarnation, WriterTicketID: issued.WriterTicketID,
			WriterFence: issued.WriterFence, PodUID: issued.PodUID,
		})
		if err != nil {
			return nil, err
		}
		proof := output.DrainedWriter{WriterTicketID: ticket, Closed: closed, PodUID: ack.PodUID, ContainersTerminated: true}
		if err := proof.Validate(); err != nil {
			return nil, err
		}
		drained = append(drained, proof)
	}
	return drained, nil
}

func (drain *OutputDrain) podForUID(ctx context.Context, node string, uid executioncontrol.PodUID) (*corev1.Pod, error) {
	pods, err := drain.Client.CoreV1().Pods(drain.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", node).String(),
	})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		if executioncontrol.PodUID(pods.Items[i].UID) == uid {
			return &pods.Items[i], nil
		}
	}
	return nil, nil
}

func allPodContainersTerminated(pod *corev1.Pod) bool {
	if len(pod.Spec.Containers) == 0 || (pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed) {
		return false
	}
	terminated := func(names []string, statuses []corev1.ContainerStatus) bool {
		if len(names) != len(statuses) {
			return false
		}
		seen := make(map[string]bool, len(statuses))
		for _, status := range statuses {
			if status.State.Terminated == nil || seen[status.Name] {
				return false
			}
			seen[status.Name] = true
		}
		for _, name := range names {
			if !seen[name] {
				return false
			}
		}
		return true
	}
	names := func(containers []corev1.Container) []string {
		out := make([]string, len(containers))
		for i, c := range containers {
			out[i] = c.Name
		}
		return out
	}
	ephemeral := make([]string, len(pod.Spec.EphemeralContainers))
	for i, c := range pod.Spec.EphemeralContainers {
		ephemeral[i] = c.Name
	}
	return terminated(names(pod.Spec.Containers), pod.Status.ContainerStatuses) &&
		terminated(names(pod.Spec.InitContainers), pod.Status.InitContainerStatuses) &&
		terminated(ephemeral, pod.Status.EphemeralContainerStatuses)
}

// ReleaseDrain is called after the daemon acknowledges source release, before
// the durable handoff is marked released. Repeating it after response loss is safe.
func (drain *OutputDrain) ReleaseDrain(ctx context.Context, node string, id executioncontrol.Identity, handoff output.HandoffID) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	control, err := drain.Controls.ForNode(ctx, node)
	if err != nil {
		return err
	}
	hold, err := control.InspectHold(ctx, id, handoff)
	if errors.Is(err, output.ErrNotFound) {
		// Cancellation may release a reservation before its hold.
		return nil
	}
	if err != nil {
		return err
	}
	pod, err := drain.podForUID(ctx, node, hold.PodUID)
	if err != nil || pod == nil {
		return err
	}
	if !slices.Contains(pod.Finalizers, outputDrainFinalizer) {
		return nil
	}
	pod.Finalizers = slices.DeleteFunc(pod.Finalizers, func(name string) bool { return name == outputDrainFinalizer })
	_, err = drain.Client.CoreV1().Pods(drain.Namespace).Update(ctx, pod, metav1.UpdateOptions{})
	return err
}
