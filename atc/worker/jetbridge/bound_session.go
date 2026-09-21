package jetbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ExecBoundSession is a transport for an already-authorized, one-use session.
// Its caller owns Run/principal authorization and supplies a fixed helper, not
// a command taken from the request. Stdin is never put in a Kubernetes object.
// The installed deadline survives loss of this controller. Actual termination
// and memory cleanup still require kubelet observation; a patch is not proof.
func (s *OutputSource) ExecBoundSession(ctx context.Context, node string, start executioncontrol.Acknowledgement, lifetime time.Duration, command []string, stdin io.Reader, stdout io.Writer) error {
	if s.executor == nil || lifetime <= 0 || len(command) == 0 || stdin == nil || stdout == nil {
		return output.ErrIncomplete
	}
	if err := start.Validate(); err != nil {
		return err
	}
	if start.Kind != executioncontrol.AcknowledgementStart {
		return output.ErrInvalidIdentity
	}
	client, err := s.recoveryClient(ctx, node, string(start.NodeUID), start.ActivationEpoch)
	if err != nil {
		return err
	}
	current, err := client.Classify(ctx, start.Identity)
	if err != nil {
		return err
	}
	if err := current.Validate(); err != nil {
		return err
	}
	if current.Identity != start.Identity || current.Classification != executioncontrol.ClassificationExecuting {
		return fmt.Errorf("%w: session execution is not running", output.ErrConflict)
	}
	// Classification already proved a start exists. This can only replay that
	// start, and refuses another Pod/process instead of authorizing a new one.
	original, err := client.RecordStart(ctx, start.Identity, start.PodUID, start.ProcessIdentity)
	if err != nil {
		return err
	}
	if original != start {
		return output.ErrInvalidIdentity
	}
	pods, err := s.client.CoreV1().Pods(s.controls.config.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return err
	}
	var pod *corev1.Pod
	for index := range pods.Items {
		if string(pods.Items[index].UID) == string(start.PodUID) {
			pod = &pods.Items[index]
			break
		}
	}
	if pod == nil {
		return fmt.Errorf("%w: original session Pod is absent", output.ErrUnresolved)
	}
	containerID, err := liveSessionContainer(pod)
	if err != nil {
		return err
	}
	seconds := int64((lifetime-1)/time.Second) + 1
	if pod.Spec.ActiveDeadlineSeconds != nil && *pod.Spec.ActiveDeadlineSeconds < seconds {
		seconds = *pod.Spec.ActiveDeadlineSeconds
	}
	deadline := pod.Status.StartTime.Add(time.Duration(seconds) * time.Second)
	if !time.Now().Before(deadline) {
		return fmt.Errorf("%w: session lifetime expired", output.ErrConflict)
	}
	if pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds > seconds {
		patch, err := json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": string(pod.UID)},
			{"op": "test", "path": "/metadata/resourceVersion", "value": pod.ResourceVersion},
			{"op": "add", "path": "/spec/activeDeadlineSeconds", "value": seconds},
		})
		if err != nil {
			return err
		}
		pod, err = s.client.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.JSONPatchType, patch, metav1.PatchOptions{})
		if err != nil {
			return err
		}
	}
	if err := s.checkBoundSession(ctx, pod.Name, node, start, containerID, seconds, deadline); err != nil {
		return err
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	if err := s.executor.ExecInPod(ctx, pod.Namespace, pod.Name, mainContainerName, command, stdin, stdout, nil, false, ExecAttrs{Purpose: "session-handoff"}); err != nil {
		return err
	}
	// Exec addresses a name. Refuse a successful response if it crossed a Pod
	// or node replacement; the caller must then treat readiness as uncertain.
	return s.checkBoundSession(ctx, pod.Name, node, start, containerID, seconds, deadline)
}

func (s *OutputSource) checkBoundSession(ctx context.Context, name, node string, start executioncontrol.Acknowledgement, containerID string, seconds int64, deadline time.Time) error {
	pod, err := s.client.CoreV1().Pods(s.controls.config.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if string(pod.UID) != string(start.PodUID) || pod.Spec.NodeName != node {
		return output.ErrInvalidIdentity
	}
	actual, err := liveSessionContainer(pod)
	if err != nil {
		return err
	}
	if actual != containerID || pod.Spec.ActiveDeadlineSeconds == nil || *pod.Spec.ActiveDeadlineSeconds > seconds ||
		pod.Status.StartTime.Add(time.Duration(*pod.Spec.ActiveDeadlineSeconds)*time.Second).After(deadline) {
		return fmt.Errorf("%w: session container or deadline changed", output.ErrConflict)
	}
	_, err = s.recoveryClient(ctx, node, string(start.NodeUID), start.ActivationEpoch)
	return err
}

func liveSessionContainer(pod *corev1.Pod) (string, error) {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Status.StartTime == nil || pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		return "", fmt.Errorf("%w: session Pod is not live", output.ErrUnresolved)
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == mainContainerName && status.State.Running != nil && status.RestartCount == 0 && status.ContainerID != "" {
			return status.ContainerID, nil
		}
	}
	return "", fmt.Errorf("%w: original session container is not running", output.ErrUnresolved)
}
