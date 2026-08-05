package jetbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/runtime"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const checkpointRestoreAnnotationKey = "concourse.ci/checkpoint-restore"

var _ runtime.PreLaunchMaterializer = (*Container)(nil)

// MaterializeBeforeLaunch is intentionally usable only by the fresh recovery
// descriptor. It pins the one scheduled Pod UID/node and either restores while
// the gate proves every reader unstarted, or verifies an already-published
// marker after a web crash. It never creates/replaces a Pod or retries another
// daemon/node.
func (c *Container) MaterializeBeforeLaunch(ctx context.Context, spec runtime.ProcessSpec) error {
	return c.materializeBeforeLaunch(ctx, spec)
}

func (c *Container) materializeBeforeLaunch(ctx context.Context, spec runtime.ProcessSpec) error {
	descriptor := c.containerSpec.CheckpointRestore
	if descriptor == nil {
		return nil
	}
	if _, _, err := c.checkpointRestoreGate(); err != nil {
		return err
	}
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) && !c.reused {
		pod, err = c.createPausePod(ctx, spec)
	}
	if err != nil {
		return fmt.Errorf("get or create exact recovery pod: %w", err)
	}
	if pod.Name != c.podName || pod.UID == "" || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || pod.Annotations[checkpointRestoreAnnotationKey] != checkpointRestoreAnnotation(c.handle, *descriptor) {
		return fmt.Errorf("recovery pod identity or descriptor is unsafe")
	}
	pod, mode, err := c.waitForRecoveryGate(ctx, pod.UID, pod.Spec.NodeName)
	if err != nil {
		return err
	}
	topology, err := runtime.CheckpointRestoreTopologyForSpec(c.containerSpec)
	if err != nil {
		return err
	}
	request := checkpoint.RestoreRequest{ContainerHandle: c.handle, MaterializationID: descriptor.MaterializationID, PodUID: string(pod.UID), Archive: descriptor.Archive, WorkspaceRoots: topology.WorkspaceRoots, SessionRoots: topology.SessionRoots, MaxBytes: descriptor.MaxBytes, MaxEntries: descriptor.MaxEntries}
	if err := request.Validate(); err != nil {
		return err
	}
	backend := c.storageBackend
	if backend == nil || backend.restoreClient == nil {
		return fmt.Errorf("checkpoint restore client is unavailable")
	}
	if mode == checkpointRecoveryGateVerify {
		_, err = backend.restoreClient.VerifyCheckpointRestore(ctx, pod.Spec.NodeName, request)
	} else {
		_, err = backend.restoreClient.RestoreCheckpoint(ctx, pod.Spec.NodeName, request)
	}
	if err != nil {
		return err
	}
	_, err = c.waitForRecoveredPodRunning(ctx, pod.UID, pod.Spec.NodeName)
	if err != nil {
		return err
	}
	c.recoveryPodUID, c.recoveryNodeName, c.recoveryReady = pod.UID, pod.Spec.NodeName, true
	return nil
}

func (c *Container) waitForRecoveryGate(ctx context.Context, uid types.UID, nodeName string) (*corev1.Pod, checkpointRecoveryGateMode, error) {
	deadline := checkpointRecoveryWaitTimeout(c.config)
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	observedNode := nodeName
	for {
		pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(waitCtx, c.podName, metav1.GetOptions{})
		if err != nil {
			return nil, 0, fmt.Errorf("get exact recovery pod while waiting for gate: %w", err)
		}
		if err := c.validateRecoveryPodIdentity(pod, uid, observedNode); err != nil {
			return nil, 0, err
		}
		if observedNode == "" && pod.Spec.NodeName != "" {
			observedNode = pod.Spec.NodeName
		}
		if err := checkpointRecoveryInitFailure(pod); err != nil {
			return nil, 0, err
		}
		if pod.Spec.NodeName != "" {
			mode, gateErr := checkpointRecoveryGateState(pod)
			if gateErr == nil {
				return pod, mode, nil
			}
			if checkpointRecoveryGateStarted(pod) {
				return nil, 0, gateErr
			}
		}
		select {
		case <-waitCtx.Done():
			return nil, 0, waitCtx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *Container) waitForRecoveredPodRunning(ctx context.Context, uid types.UID, nodeName string) (*corev1.Pod, error) {
	deadline := checkpointRecoveryWaitTimeout(c.config)
	waitCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for {
		pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(waitCtx, c.podName, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get exact recovery pod while waiting for launch: %w", err)
		}
		if err := c.validateRecoveryPodIdentity(pod, uid, nodeName); err != nil {
			return nil, err
		}
		if err := checkpointRecoveryInitFailure(pod); err != nil {
			return nil, err
		}
		mode, err := checkpointRecoveryGateState(pod)
		if err != nil {
			return nil, fmt.Errorf("recovery gate did not remain safe: %w", err)
		}
		if mode == checkpointRecoveryGateVerify && pod.Status.Phase == corev1.PodRunning {
			return pod, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, waitCtx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func checkpointRecoveryWaitTimeout(config Config) time.Duration {
	startup := config.PodStartupTimeout
	if startup <= 0 {
		startup = DefaultPodStartupTimeout
	}
	scheduling := podSchedulingTimeout(config)
	if scheduling > startup {
		return scheduling
	}
	return startup
}

func (c *Container) validateRecoveryPodIdentity(pod *corev1.Pod, uid types.UID, nodeName string) error {
	if pod == nil || pod.Name != c.podName || pod.UID != uid || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || pod.Annotations[checkpointRestoreAnnotationKey] != checkpointRestoreAnnotation(c.handle, *c.containerSpec.CheckpointRestore) {
		return fmt.Errorf("recovery pod identity or descriptor changed")
	}
	if nodeName != "" && pod.Spec.NodeName != nodeName {
		return fmt.Errorf("recovery pod node changed")
	}
	return nil
}

func (c *Container) requireMaterializedRecoveryPod(ctx context.Context) error {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	if !c.recoveryReady || c.recoveryPodUID == "" || c.recoveryNodeName == "" {
		return fmt.Errorf("checkpoint recovery must materialize the exact pod before Run")
	}
	pod, err := c.clientset.CoreV1().Pods(c.config.Namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil || pod.UID != c.recoveryPodUID || pod.Spec.NodeName != c.recoveryNodeName || pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded || pod.Annotations[checkpointRestoreAnnotationKey] != checkpointRestoreAnnotation(c.handle, *c.containerSpec.CheckpointRestore) {
		return fmt.Errorf("checkpoint recovery pod no longer matches materialized identity: %w", err)
	}
	return nil
}

func (c *Container) matchesMaterializedRecoveryPod(pod *corev1.Pod) bool {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()
	return c.recoveryReady && pod != nil && pod.UID == c.recoveryPodUID && pod.Spec.NodeName == c.recoveryNodeName && pod.Annotations[checkpointRestoreAnnotationKey] == checkpointRestoreAnnotation(c.handle, *c.containerSpec.CheckpointRestore)
}

type checkpointRecoveryGateMode int

const (
	checkpointRecoveryGateRestore checkpointRecoveryGateMode = iota
	checkpointRecoveryGateVerify
)

func checkpointRecoveryGateState(pod *corev1.Pod) (checkpointRecoveryGateMode, error) {
	gateIndex := -1
	for index, container := range pod.Spec.InitContainers {
		if container.Name == checkpointRestoreGateInitName {
			gateIndex = index
			break
		}
	}
	if gateIndex < 0 || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return 0, fmt.Errorf("recovery gate is not present in a live exact pod")
	}
	statuses := make(map[string]corev1.ContainerStatus, len(pod.Status.InitContainerStatuses))
	for _, status := range pod.Status.InitContainerStatuses {
		statuses[status.Name] = status
	}
	gate, found := statuses[checkpointRestoreGateInitName]
	if !found {
		return 0, fmt.Errorf("recovery gate has not started")
	}
	if gate.RestartCount != 0 || gate.LastTerminationState.Terminated != nil {
		return 0, fmt.Errorf("recovery gate restarted")
	}
	for index, init := range pod.Spec.InitContainers {
		if index >= gateIndex {
			break
		}
		status, present := statuses[init.Name]
		if !present || status.State.Terminated == nil || status.State.Terminated.ExitCode != 0 || status.RestartCount != 0 {
			return 0, fmt.Errorf("recovery gate preceding init %q has not succeeded", init.Name)
		}
	}
	if gate.State.Terminated != nil && gate.State.Terminated.ExitCode == 0 {
		return checkpointRecoveryGateVerify, nil
	}
	if pod.Status.Phase != corev1.PodPending {
		return 0, fmt.Errorf("recovery gate is not pending for a restore")
	}
	if gate.State.Running == nil {
		return 0, fmt.Errorf("recovery gate is not safely waiting")
	}
	for index, init := range pod.Spec.InitContainers {
		status := statuses[init.Name]
		if index > gateIndex && (status.State.Running != nil || status.State.Terminated != nil) {
			return 0, fmt.Errorf("recovery gate later init %q already started", init.Name)
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Running != nil || status.State.Terminated != nil {
			return 0, fmt.Errorf("recovery gate regular container %q already started", status.Name)
		}
	}
	for _, status := range pod.Status.EphemeralContainerStatuses {
		if status.State.Running != nil || status.State.Terminated != nil {
			return 0, fmt.Errorf("recovery gate ephemeral container %q already started", status.Name)
		}
	}
	return checkpointRecoveryGateRestore, nil
}

func checkpointRecoveryGateStarted(pod *corev1.Pod) bool {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name == checkpointRestoreGateInitName {
			return status.State.Running != nil || status.State.Terminated != nil || status.RestartCount != 0
		}
	}
	return false
}

func checkpointRecoveryInitFailure(pod *corev1.Pod) error {
	for _, status := range pod.Status.InitContainerStatuses {
		if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			return fmt.Errorf("recovery init %q failed", status.Name)
		}
	}
	return nil
}

func checkpointRestoreAnnotation(handle string, descriptor runtime.CheckpointRestoreDescriptor) string {
	encoded, _ := json.Marshal(struct {
		Handle string
		runtime.CheckpointRestoreDescriptor
	}{handle, descriptor})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
