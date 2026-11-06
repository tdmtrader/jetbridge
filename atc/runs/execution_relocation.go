package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/output"
)

// ErrRelocationUnavailable is a relocation this starter cannot make: the
// container is no Run execution, or the node plane cannot select another node.
// The caller keeps the pod where it is.
var ErrRelocationUnavailable = errors.New("this execution cannot be moved to another node")

// ExecutionRelocator is the node plane's half of a relocation: choosing a node
// other than the ones a step could not be scheduled on, and minting a capture's
// hold grant on the node it moves to.
type ExecutionRelocator interface {
	SelectNodeExcept(context.Context, runtime.ContainerSpec, []string) (string, string, error)
	RuntimeControl(context.Context, output.Capture) (*runtime.ExecutionControl, error)
}

// RelocateContainer moves an admitted execution whose pod was never bound to
// its node onto another node, and returns the spec its replacement pod is built
// from. The caller has already proved the pod never ran -- it deleted the pod
// with a precondition on an observation that showed it unbound -- so the
// execution was never started: no exec can reach a pod that never ran, and an
// exact start is only ever issued over exec.
//
// The execution keeps its identity. Its admission, and a capturing execution's
// pending capture, name the new node; the old node keeps a never-started
// execution record that nothing will start, because every start is checked
// against the admission's node (CheckStart) first. exclude names the nodes
// already tried, beside the one the spec is admitted on.
func (s *ExecutionStarter) RelocateContainer(ctx context.Context, owner db.ContainerOwner, spec runtime.ContainerSpec, exclude []string) (runtime.ContainerSpec, error) {
	buildID, planID, teamID, isBuild := db.BuildStepContainerIdentity(owner)
	control := spec.ExecutionControl
	if !isBuild || control == nil || control.Node == nil || teamID != spec.TeamID {
		return spec, ErrRelocationUnavailable
	}
	relocator, ok := s.Source.(ExecutionRelocator)
	if !ok || s.Source == nil {
		return spec, ErrRelocationUnavailable
	}
	from := *control.Node
	tried := append(append([]string{}, exclude...), from.Name)
	to, toUID, err := relocator.SelectNodeExcept(ctx, spec, tried)
	if err != nil {
		return spec, fmt.Errorf("choosing a node to move execution %s to: %w", control.Identity.ExecutionID, err)
	}

	var moved db.RunExecutionAdmission
	err = s.transaction(ctx, func(tx db.Tx) error {
		var err error
		moved, err = s.Factory.RelocateRunExecution(ctx, tx, db.RunExecutionRelocation{
			BuildID: buildID, PlanID: planID, Identity: control.Identity,
			FromName: from.Name, FromUID: string(from.UID), ToName: to, ToUID: toUID,
		})
		return err
	})
	if err != nil {
		return spec, err
	}

	next, err := s.Source.BaseRuntimeControl(ctx, moved.NodeName, moved.NodeUID, moved.Identity)
	if err != nil {
		return spec, err
	}
	if next == nil {
		return spec, ErrRelocationUnavailable
	}
	if control.HasDurableOutputCapture() {
		capture := *control.Capture
		capture.Node, capture.NodeUID = moved.NodeName, next.Node.UID
		held, err := relocator.RuntimeControl(ctx, output.Capture{
			Key: capture.Key(), Execution: capture.Identity, State: output.CapturePending,
			Node: capture.Node, NodeUID: capture.NodeUID, CaptureDeadline: capture.CaptureDeadline,
		})
		if err != nil {
			return spec, err
		}
		if !held.HasDurableOutputCapture() {
			return spec, fmt.Errorf("the node plane minted no hold grant for capture %s on node %s", capture.Key(), capture.Node)
		}
		capture.SourceControlGrant = held.Capture.SourceControlGrant
		if err = next.SelectCapture(capture); err != nil {
			return spec, err
		}
	}
	spec.ExecutionControl = next
	if err = next.Validate(spec); err != nil {
		return spec, err
	}
	return spec, s.CheckStart(ctx, owner, spec)
}
