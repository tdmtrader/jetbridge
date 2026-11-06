package jetbridge

import (
	"context"
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// OutputSource selects and addresses the exact node that owns a producer's
// source. It has no Run or database dependency and never creates a Pod.
type OutputSource struct {
	client   kubernetes.Interface
	controls *nodeOutputControls
	executor PodExecutor
}

func (s *OutputSource) SetExecutor(executor PodExecutor) { s.executor = executor }

// RecoverExecutionOutcome reads an existing journaled execution: a supervised
// task or a Run-owned resource command. Its retained start names the original
// Pod and journal; absence never permits launch.
func (s *OutputSource) RecoverExecutionOutcome(ctx context.Context, name string, start executioncontrol.Acknowledgement) (executioncontrol.ClassifyResult, error) {
	if err := start.Validate(); err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	if start.Kind != executioncontrol.AcknowledgementStart {
		return executioncontrol.ClassifyResult{}, output.ErrInvalidIdentity
	}
	state, _, err := executionJournalFromIdentity(start.ProcessIdentity)
	if err != nil {
		return executioncontrol.ClassifyResult{}, fmt.Errorf("%w: %v", output.ErrUnresolved, err)
	}
	client, err := s.recoveryClient(ctx, name, string(start.NodeUID))
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	classified, err := client.Classify(ctx, start.Identity)
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	if err := classified.Validate(); err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	if classified.Identity != start.Identity {
		return executioncontrol.ClassifyResult{}, output.ErrInvalidIdentity
	}
	if classified.Classification != executioncontrol.ClassificationExecuting {
		return classified, nil
	}
	// Start already exists. Replay against its node refuses any other Pod or
	// process locator and cannot create a new execution.
	if _, err = client.RecordStart(ctx, start.Identity, start.PodUID, start.ProcessIdentity); err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	pods, err := s.client.CoreV1().Pods(s.controls.config.Namespace).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + name})
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	for _, pod := range pods.Items {
		if string(pod.UID) != string(start.PodUID) {
			continue
		}
		outcome, err := readJournaledOutcome(ctx, s.client, s.executor, pod.Namespace, pod.Name, name, state, start)
		if err != nil {
			return executioncontrol.ClassifyResult{}, fmt.Errorf("%w: %v", output.ErrUnresolved, err)
		}
		if _, err = client.RecordOutcome(ctx, start.Identity, executioncontrol.AcknowledgementFinish, outcome); err != nil {
			return executioncontrol.ClassifyResult{}, err
		}
		return client.Classify(ctx, start.Identity)
	}
	return executioncontrol.ClassifyResult{}, fmt.Errorf("%w: original Pod is absent", output.ErrUnresolved)
}

// These recovery calls revalidate the retained node UID for every operation.
// A replacement with the same Kubernetes name cannot inherit the old source.
func (s *OutputSource) ClassifyExecution(ctx context.Context, name, uid string, id executioncontrol.Identity) (executioncontrol.ClassifyResult, error) {
	client, err := s.recoveryClient(ctx, name, uid)
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	return client.Classify(ctx, id)
}

// ExecutionStart reads the node's start for an exact execution, for a Run
// that admitted it and never retained the start the node committed. The
// answer must be for that identity and node; it is the node's over the mTLS
// channel, and the channel is what makes it so. It starts nothing.
func (s *OutputSource) ExecutionStart(ctx context.Context, name, uid string, id executioncontrol.Identity) (executioncontrol.Acknowledgement, error) {
	client, err := s.recoveryClient(ctx, name, uid)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	start, err := client.InspectStart(ctx, id)
	if err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	if err := start.Validate(); err != nil {
		return executioncontrol.Acknowledgement{}, err
	}
	if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != id || string(start.NodeUID) != uid {
		return executioncontrol.Acknowledgement{}, fmt.Errorf("%w: the node's start is not this execution's", output.ErrInvalidIdentity)
	}
	return start, nil
}

func (s *OutputSource) StopExecution(ctx context.Context, name, uid string, id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	client, err := s.recoveryClient(ctx, name, uid)
	if err != nil {
		return executioncontrol.RequestSourcePreservingStopResult{}, err
	}
	return client.RequestStop(ctx, id)
}

// CaptureControl dials the node a capture row names, refusing a node replaced
// under the same name.
func (s *OutputSource) CaptureControl(ctx context.Context, name string, uid executioncontrol.NodeUID) (hangaroutput.SourceControl, error) {
	return s.exactClient(ctx, name, string(uid))
}

func (s *OutputSource) recoveryClient(ctx context.Context, name, uid string) (*OutputControlClient, error) {
	return s.exactClient(ctx, name, uid)
}

func NewOutputSource(client kubernetes.Interface, config Config, signer *hangar.Signer) *OutputSource {
	return &OutputSource{client: client, controls: &nodeOutputControls{
		config: config, resolver: NewNodeIPResolver(client), signer: signer,
	}}
}

// SelectNode chooses the node a Run execution is admitted on. See
// selectExecutionNode for how.
func (s *OutputSource) SelectNode(ctx context.Context, spec runtime.ContainerSpec) (string, string, error) {
	return selectExecutionNode(ctx, s.client, s.controls.config, spec, nil)
}

// SelectNodeExcept chooses as SelectNode does, among the nodes not in exclude:
// the nodes a step already could not be scheduled on.
func (s *OutputSource) SelectNodeExcept(ctx context.Context, spec runtime.ContainerSpec, exclude []string) (string, string, error) {
	return selectExecutionNode(ctx, s.client, s.controls.config, spec, exclude)
}

func (s *OutputSource) exactClient(ctx context.Context, name, uid string) (*OutputControlClient, error) {
	node, err := s.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: output node %s is gone: %v", output.ErrNotFound, name, err)
	}
	if err != nil {
		return nil, err
	}
	if uid == "" || string(node.UID) != uid {
		return nil, fmt.Errorf("%w: output node has been replaced", output.ErrInvalidIdentity)
	}
	return s.controls.clientForNode(ctx, name)
}

// RuntimeControl admits a capture's exact execution on the capture's node and
// composes the envelope its Pod is built from: the base grant, and the capture
// control init's one hold grant. The node must be the one the capture row
// names, by name and by UID; the step directory is derived, not chosen.
func (s *OutputSource) RuntimeControl(ctx context.Context, capture output.Capture) (*runtime.ExecutionControl, error) {
	if capture.State != output.CapturePending {
		return nil, fmt.Errorf("%w: capture %s is %s; only a pending capture admits a producer",
			output.ErrConflict, capture.Key, capture.State)
	}
	client, err := s.exactClient(ctx, capture.Node, string(capture.NodeUID))
	if err != nil {
		return nil, err
	}
	baseGrant, err := client.MintGrant(hangar.PurposeControlBase, "observe", capture.Execution)
	if err != nil {
		return nil, err
	}
	if _, err := client.Admit(ctx, executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion, Identity: capture.Execution,
		NodeUID: capture.NodeUID, Capability: baseGrant,
	}); err != nil {
		return nil, err
	}
	holdGrant, err := client.MintGrant(hangar.PurposeControlCapture, "hold", capture.Execution)
	if err != nil {
		return nil, err
	}
	control := &runtime.ExecutionControl{
		Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted,
		Identity: capture.Execution,
		Endpoint: client.endpoint, Capability: baseGrant,
	}
	err = control.SelectCapture(runtime.DurableOutputCapture{
		Version: runtime.DurableOutputCaptureVersion, Identity: capture.Execution,
		Output:             string(capture.Key.Output),
		SourceControlGrant: holdGrant, CaptureDeadline: capture.CaptureDeadline,
		Node: capture.Node, NodeUID: capture.NodeUID,
	})
	return control, err
}

// BaseRuntimeControl admits the original exact identity before Pod creation.
// Repeating it reconciles an unanswered admission; it starts no process.
func (s *OutputSource) BaseRuntimeControl(ctx context.Context, name, uid string, id executioncontrol.Identity) (*runtime.ExecutionControl, error) {
	client, err := s.recoveryClient(ctx, name, uid)
	if err != nil {
		return nil, err
	}
	grant, err := client.MintGrant(hangar.PurposeControlBase, "observe", id)
	if err != nil {
		return nil, err
	}
	if _, err = client.Admit(ctx, executioncontrol.Envelope{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id, NodeUID: executioncontrol.NodeUID(uid), Capability: grant}); err != nil {
		return nil, err
	}
	return &runtime.ExecutionControl{Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted, Identity: id, Endpoint: client.endpoint, Capability: grant, Node: &runtime.ExecutionNode{Name: name, UID: executioncontrol.NodeUID(uid)}}, nil
}

// StatTaskInput asks the selected node for exact managed object metadata. The
// web process neither opens the object store nor materializes repository bytes.
//
// Only the node's stat may answer output.ErrNotFound: the read admission
// records that answer as an out-of-band absence of the generation. A selected
// node that is gone, before or after the stat, is this exact node no longer
// existing -- as a replaced node is -- and says nothing about the store.
func (s *OutputSource) StatTaskInput(ctx context.Context, name, uid string, ref hangar.TreeRef) (output.PublishedObject, error) {
	client, err := s.taskInputNode(ctx, name, uid)
	if err != nil {
		return output.PublishedObject{}, err
	}
	object, err := client.StatExactObject(ctx, ref)
	if err != nil {
		return output.PublishedObject{}, err
	}
	if _, err := s.taskInputNode(ctx, name, uid); err != nil {
		return output.PublishedObject{}, err
	}
	return object, nil
}

func (s *OutputSource) taskInputNode(ctx context.Context, name, uid string) (*OutputControlClient, error) {
	client, err := s.recoveryClient(ctx, name, uid)
	if errors.Is(err, output.ErrNotFound) {
		return nil, fmt.Errorf("%w: the task's selected node is gone: %v", output.ErrInvalidIdentity, err)
	}
	return client, err
}
