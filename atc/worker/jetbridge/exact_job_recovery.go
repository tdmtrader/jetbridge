package jetbridge

// A Run's cancellation and recovery of an exact task started on the job step
// runtime: its retained start names job-v1:<step Job UID> rather than a
// journal in a pause pod. The protocol is the exec runtime's
// (process_interrupt.go, output_source.go); what differs is how the command
// is interrupted -- its pod's deadline cut to now, never an exec, never a
// delete -- and where its outcome is read: main's terminated status and the
// launcher's record on the pod the start names.

import (
	"context"
	"fmt"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// interruptJobExecution stops the command a job-runtime start names: the
// node's stop request first, then the pod. Requesting a stop is not an
// outcome; recoverJobExecutionOutcome reads one once the command has ended.
func (s *OutputSource) interruptJobExecution(ctx context.Context, node string, start executioncontrol.Acknowledgement, job types.UID) error {
	client, classified, err := s.executingJobStart(ctx, node, start)
	if err != nil || classified.Classification != executioncontrol.ClassificationExecuting {
		return err
	}
	stop, err := client.RequestStop(ctx, start.Identity)
	if err != nil {
		return err
	}
	if !stop.Accepted {
		return output.ErrUnresolved
	}
	pod, err := s.startedJobPod(ctx, node, start, job)
	if err != nil {
		return err
	}
	return stopStepJobPod(ctx, s.client, pod.Namespace, pod.Name, pod.UID)
}

// recoverJobExecutionOutcome records the outcome of a job-runtime start from
// the pod it names, once that pod's command has ended with the launcher's
// record. A command still running, a pod gone, or an exit nothing
// corroborates is unresolved; nothing is ever run.
func (s *OutputSource) recoverJobExecutionOutcome(ctx context.Context, node string, start executioncontrol.Acknowledgement, job types.UID) (executioncontrol.ClassifyResult, error) {
	client, classified, err := s.executingJobStart(ctx, node, start)
	if err != nil || classified.Classification != executioncontrol.ClassificationExecuting {
		return classified, err
	}
	pod, err := s.startedJobPod(ctx, node, start, job)
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	exit, err := jobPodExit(pod)
	if err != nil {
		return executioncontrol.ClassifyResult{}, fmt.Errorf("%w: %v", output.ErrUnresolved, err)
	}
	if _, err := client.RecordOutcome(ctx, start.Identity, executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: exit}); err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	return client.Classify(ctx, start.Identity)
}

// executingJobStart classifies the start's execution at its node and, while
// it is executing, has the node replay the start: it refuses any other pod or
// process and cannot create one.
func (s *OutputSource) executingJobStart(ctx context.Context, node string, start executioncontrol.Acknowledgement) (*OutputControlClient, executioncontrol.ClassifyResult, error) {
	client, err := s.recoveryClient(ctx, node, string(start.NodeUID))
	if err != nil {
		return nil, executioncontrol.ClassifyResult{}, err
	}
	classified, err := client.Classify(ctx, start.Identity)
	if err != nil {
		return nil, executioncontrol.ClassifyResult{}, err
	}
	if err := classified.Validate(); err != nil {
		return nil, executioncontrol.ClassifyResult{}, err
	}
	if classified.Identity != start.Identity {
		return nil, executioncontrol.ClassifyResult{}, output.ErrInvalidIdentity
	}
	if classified.Classification != executioncontrol.ClassificationExecuting {
		return client, classified, nil
	}
	original, err := client.RecordStart(ctx, start.Identity, start.PodUID, start.ProcessIdentity)
	if err != nil {
		return nil, executioncontrol.ClassifyResult{}, err
	}
	if original != start {
		return nil, executioncontrol.ClassifyResult{}, output.ErrInvalidIdentity
	}
	return client, classified, nil
}

// startedJobPod is the pod the start names, on its node, owned by the step
// Job the start's process identity names.
func (s *OutputSource) startedJobPod(ctx context.Context, node string, start executioncontrol.Acknowledgement, job types.UID) (*corev1.Pod, error) {
	pods, err := s.client.CoreV1().Pods(s.controls.config.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
	if err != nil {
		return nil, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if string(pod.UID) != string(start.PodUID) {
			continue
		}
		if controllingJob(pod) != job {
			return nil, fmt.Errorf("%w: pod %s is not owned by step job %s", output.ErrInvalidIdentity, pod.Name, job)
		}
		return pod, nil
	}
	return nil, fmt.Errorf("%w: the started step job's pod is absent", output.ErrUnresolved)
}
