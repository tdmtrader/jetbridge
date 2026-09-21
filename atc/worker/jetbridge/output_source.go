package jetbridge

import (
	"context"
	"fmt"
	"sort"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
// task or a Run-owned resource command. Its signed start names the original
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
	client, err := s.recoveryClient(ctx, name, string(start.NodeUID), start.ActivationEpoch)
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
func (s *OutputSource) ClassifyExecution(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (executioncontrol.ClassifyResult, error) {
	client, err := s.recoveryClient(ctx, name, uid, epoch)
	if err != nil {
		return executioncontrol.ClassifyResult{}, err
	}
	return client.Classify(ctx, id)
}

// ExecutionStart reads the node's signed start for an exact execution, for a
// Run that admitted it and never retained the start the node committed. The
// answer must be for that identity, node and epoch; the caller still verifies
// the node's signature before it retains anything. It starts nothing.
func (s *OutputSource) ExecutionStart(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (executioncontrol.Acknowledgement, error) {
	client, err := s.recoveryClient(ctx, name, uid, epoch)
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
	if start.Kind != executioncontrol.AcknowledgementStart || start.Identity != id ||
		start.ActivationEpoch != epoch || string(start.NodeUID) != uid {
		return executioncontrol.Acknowledgement{}, fmt.Errorf("%w: the node's start is not this execution's", output.ErrInvalidIdentity)
	}
	return start, nil
}

func (s *OutputSource) StopExecution(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	client, err := s.recoveryClient(ctx, name, uid, epoch)
	if err != nil {
		return executioncontrol.RequestSourcePreservingStopResult{}, err
	}
	return client.RequestStop(ctx, id)
}

func (s *OutputSource) CaptureControl(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch) (hangaroutput.SourceControl, error) {
	return s.recoveryClient(ctx, name, uid, epoch)
}

func (s *OutputSource) recoveryClient(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch) (*OutputControlClient, error) {
	if epoch != s.controls.epoch {
		return nil, fmt.Errorf("%w: source epoch differs from runtime", output.ErrConflict)
	}
	return s.exactClient(ctx, name, uid)
}

func NewOutputSource(client kubernetes.Interface, config Config, minter *executioncontrol.CapabilityMinter, epoch executioncontrol.ActivationEpoch) *OutputSource {
	return &OutputSource{client: client, controls: &nodeOutputControls{
		config: config, resolver: NewNodeIPResolver(client), minter: minter, epoch: epoch,
	}}
}

func (s *OutputSource) SelectNode(ctx context.Context, spec runtime.ContainerSpec) (string, string, error) {
	selector := labels.Set{
		"concourse.dev/artifact-cache": "ready",
		executioncontrol.ReadyLabel:    "ready", output.ReadyLabel: "ready",
	}
	for _, input := range spec.Inputs {
		if input.HangarTree != nil {
			selector["concourse.dev/hangar-v1"] = "ready"
		}
	}
	nodes, err := s.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return "", "", err
	}
	sort.Slice(nodes.Items, func(i, j int) bool { return nodes.Items[i].Name < nodes.Items[j].Name })
	for _, node := range nodes.Items {
		if node.Spec.Unschedulable || node.DeletionTimestamp != nil || node.Labels[corev1.LabelHostname] != node.Name {
			continue
		}
		tainted := false
		for _, taint := range node.Spec.Taints {
			if taint.Effect == corev1.TaintEffectNoSchedule || taint.Effect == corev1.TaintEffectNoExecute {
				tainted = true
			}
		}
		if tainted {
			continue
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				return node.Name, string(node.UID), nil
			}
		}
	}
	return "", "", fmt.Errorf("%w: no ready output node", output.ErrInfrastructure)
}

func (s *OutputSource) exactClient(ctx context.Context, name, uid string) (*OutputControlClient, error) {
	node, err := s.client.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if uid == "" || string(node.UID) != uid {
		return nil, fmt.Errorf("%w: output node has been replaced", output.ErrInvalidIdentity)
	}
	return s.controls.clientForNode(ctx, name)
}

func (s *OutputSource) ReserveSource(ctx context.Context, name, uid string, admission output.CaptureAdmission) (output.ReservedIncarnation, error) {
	if err := admission.Validate(); err != nil {
		return output.ReservedIncarnation{}, err
	}
	if admission.ActivationEpoch != s.controls.epoch {
		return output.ReservedIncarnation{}, fmt.Errorf("%w: source epoch differs from runtime", output.ErrConflict)
	}
	client, err := s.exactClient(ctx, name, uid)
	if err != nil {
		return output.ReservedIncarnation{}, err
	}
	capability, err := client.MintGrant(executioncontrol.BaseFacet, "observe", admission.Execution)
	if err != nil {
		return output.ReservedIncarnation{}, err
	}
	if _, err := client.Admit(ctx, executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion, Identity: admission.Execution,
		ActivationEpoch: admission.ActivationEpoch, NodeUID: executioncontrol.NodeUID(uid), Capability: capability,
	}); err != nil {
		return output.ReservedIncarnation{}, err
	}
	return client.ReserveIncarnation(ctx, admission)
}

func (s *OutputSource) RuntimeControl(ctx context.Context, name string, record output.HandoffRecord) (*runtime.ExecutionControl, error) {
	if !record.Source.Reserved() || record.Source.Locator != name || record.ActivationEpoch != s.controls.epoch {
		return nil, fmt.Errorf("%w: no matching reserved runtime source", output.ErrInvalidIdentity)
	}
	client, err := s.exactClient(ctx, name, string(record.Source.Incarnation.NodeUID))
	if err != nil {
		return nil, err
	}
	baseGrant, err := client.MintGrant(executioncontrol.BaseFacet, "observe", record.Execution)
	if err != nil {
		return nil, err
	}
	holdGrant, err := client.MintGrant(output.CaptureFacet, "hold", record.Execution)
	if err != nil {
		return nil, err
	}
	control := &runtime.ExecutionControl{
		Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted,
		Identity: record.Execution, ActivationEpoch: record.ActivationEpoch,
		Endpoint: client.endpoint, Capability: baseGrant,
	}
	err = control.SelectCapture(runtime.DurableOutputCapture{
		Version: runtime.DurableOutputCaptureVersion, Identity: record.Execution, ActivationEpoch: record.ActivationEpoch,
		HandoffID: record.HandoffID, SourceHoldID: record.SourceHoldID, Output: string(record.Output),
		SourceControlGrant: holdGrant, CaptureDeadline: record.CaptureDeadline.Time,
		ReservedIncarnation: record.Source.Incarnation, ReservedDirectory: record.Source.Directory, ReservingNode: name,
	})
	return control, err
}

// BaseRuntimeControl admits the original exact identity before Pod creation.
// Repeating it reconciles an unanswered admission; it starts no process.
func (s *OutputSource) BaseRuntimeControl(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, id executioncontrol.Identity) (*runtime.ExecutionControl, error) {
	client, err := s.recoveryClient(ctx, name, uid, epoch)
	if err != nil {
		return nil, err
	}
	grant, err := client.MintGrant(executioncontrol.BaseFacet, "observe", id)
	if err != nil {
		return nil, err
	}
	if _, err = client.Admit(ctx, executioncontrol.Envelope{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id, ActivationEpoch: epoch, NodeUID: executioncontrol.NodeUID(uid), Capability: grant}); err != nil {
		return nil, err
	}
	return &runtime.ExecutionControl{Version: runtime.ExecutionControlVersion, Phase: runtime.ControlPhaseAdmitted, Identity: id, ActivationEpoch: epoch, Endpoint: client.endpoint, Capability: grant, Node: &runtime.ExecutionNode{Name: name, UID: executioncontrol.NodeUID(uid)}}, nil
}

// StatTaskInput asks the selected node for exact managed object metadata. The
// web process neither opens the object store nor materializes repository bytes.
func (s *OutputSource) StatTaskInput(ctx context.Context, name, uid string, epoch executioncontrol.ActivationEpoch, ref hangar.TreeRef) (output.PublishedObject, error) {
	client, err := s.recoveryClient(ctx, name, uid, epoch)
	if err != nil {
		return output.PublishedObject{}, err
	}
	object, err := client.StatExactObject(ctx, ref)
	if err != nil {
		return output.PublishedObject{}, err
	}
	if _, err := s.exactClient(ctx, name, uid); err != nil {
		return output.PublishedObject{}, err
	}
	return object, nil
}
