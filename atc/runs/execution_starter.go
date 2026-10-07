package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

type ExecutionSource interface {
	SelectNode(context.Context, runtime.ContainerSpec) (string, string, error)
	BaseRuntimeControl(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (*runtime.ExecutionControl, error)
}

// ExecutionStarter supplies the Run gate at the shared worker boundary. Node
// capabilities remain transient and the database retains only attribution.
type ExecutionStarter struct {
	Conn            db.DbConn
	Factory         db.PipelineRunFactory
	Source          ExecutionSource
	Epoch           executioncontrol.ActivationEpoch
	Verifier        db.RunExecutionVerifier
	Output          *OutputStarter
	inputReadMinter hangaroutput.WarrantMinter
}

func (s *ExecutionStarter) PrepareContainer(ctx context.Context, owner db.ContainerOwner, metadata db.ContainerMetadata, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	buildID, planID, teamID, isBuild := db.BuildStepContainerIdentity(owner)
	if !isBuild {
		buildID = metadata.BuildID
	}
	var owned, found bool
	var existing db.RunExecutionAdmission
	err := s.transaction(ctx, func(tx db.Tx) error {
		_, yes, err := s.Factory.RunExecutionOwner(ctx, tx, buildID)
		owned = yes
		if err != nil || !owned {
			return err
		}
		existing, found, err = s.Factory.RunExecution(ctx, tx, buildID, planID)
		return err
	})
	if err != nil || !owned {
		return spec, err
	}
	if !isBuild || teamID != spec.TeamID || (metadata.BuildID != 0 && metadata.BuildID != buildID) || metadata.Type != spec.Type || s.Source == nil || s.Epoch == 0 || s.Verifier == nil {
		return spec, atc.ErrRunResultsUnavailable
	}
	req := db.RunExecutionRequest{BuildID: buildID, PlanID: planID, Kind: spec.Type, Epoch: int64(s.Epoch)}
	if found {
		req.NodeName, req.NodeUID = existing.NodeName, existing.NodeUID
	}
	if spec.ExecutionControl.HasDurableOutputCapture() {
		capture := spec.ExecutionControl.Capture
		req.Capture = capture.Key()
		if !found {
			req.NodeName, req.NodeUID = capture.Node, string(capture.NodeUID)
		}
	} else if !found {
		req.NodeName, req.NodeUID, err = s.Source.SelectNode(ctx, spec)
		if err != nil {
			return spec, err
		}
	}
	var admission db.RunExecutionAdmission
	err = s.transaction(ctx, func(tx db.Tx) error {
		var err error
		admission, _, err = s.Factory.AdmitRunExecution(ctx, tx, req)
		return err
	})
	if err != nil {
		return spec, err
	}
	if spec.ExecutionControl != nil && spec.ExecutionControl.Identity != admission.Identity {
		return spec, errors.New("Run execution control does not match its admission")
	}
	control, err := s.Source.BaseRuntimeControl(ctx, req.NodeName, req.NodeUID, s.Epoch, admission.Identity)
	if err != nil {
		return spec, err
	}
	if control == nil {
		return spec, atc.ErrRunResultsUnavailable
	}
	if spec.ExecutionControl.HasDurableOutputCapture() {
		capture := *spec.ExecutionControl.Capture
		if err = control.SelectCapture(capture); err != nil {
			return spec, err
		}
	}
	spec.ExecutionControl = control
	if err = control.Validate(spec); err != nil {
		return spec, err
	}
	// A cancellation may have committed while the node was being contacted.
	return spec, s.CheckStart(ctx, owner, spec)
}

func (s *ExecutionStarter) RecordWitness(ctx context.Context, owner db.ContainerOwner, witness executioncontrol.Acknowledgement) error {
	buildID, planID, _, isBuild := db.BuildStepContainerIdentity(owner)
	if !isBuild {
		return nil
	}
	recorded := false
	err := s.transaction(ctx, func(tx db.Tx) error {
		_, owned, err := s.Factory.RunExecutionOwner(ctx, tx, buildID)
		if err != nil || !owned {
			return err
		}
		if err := s.Factory.RecordRunExecutionWitness(ctx, tx, buildID, planID, witness, s.Verifier); err != nil {
			return err
		}
		recorded = true
		return nil
	})
	if err == nil && recorded && witness.Kind == executioncontrol.AcknowledgementStart {
		// Off the start path: the release takes Hangar suffix locks, and
		// contention on them must not delay the command's exec.
		go s.releaseInputReads(context.WithoutCancel(ctx), buildID, planID)
	}
	return err
}

func (s *ExecutionStarter) CheckStart(ctx context.Context, owner db.ContainerOwner, spec runtime.ContainerSpec) error {
	buildID, planID, teamID, isBuild := db.BuildStepContainerIdentity(owner)
	if !isBuild {
		return nil
	}
	return s.transaction(ctx, func(tx db.Tx) error {
		_, owned, err := s.Factory.RunExecutionOwner(ctx, tx, buildID)
		if err != nil || !owned {
			return err
		}
		control := spec.ExecutionControl
		if control == nil || control.Node == nil || teamID != spec.TeamID {
			return atc.ErrRunResultsUnavailable
		}
		req := db.RunExecutionRequest{BuildID: buildID, PlanID: planID, Kind: spec.Type, Epoch: int64(control.ActivationEpoch), NodeName: control.Node.Name, NodeUID: string(control.Node.UID)}
		if control.HasDurableOutputCapture() {
			req.Capture = control.Capture.Key()
		}
		admission, _, err := s.Factory.AdmitRunExecution(ctx, tx, req)
		if err != nil {
			return err
		}
		if admission.Identity != control.Identity {
			return atc.ErrRunResultsUnavailable
		}
		return nil
	})
}

func (s *ExecutionStarter) CheckIntercept(ctx context.Context, handle string) error {
	return s.transaction(ctx, func(tx db.Tx) error {
		owned, err := s.Factory.RunExecutionContainer(ctx, tx, handle)
		if err != nil {
			return err
		}
		if owned {
			return fmt.Errorf("%w: Run containers cannot admit an untracked intercepted command", atc.ErrRunResultsUnavailable)
		}
		return nil
	})
}

func (s *ExecutionStarter) transaction(ctx context.Context, f func(db.Tx) error) error {
	if s.Conn == nil || s.Factory == nil {
		return atc.ErrRunResultsUnavailable
	}
	tx, err := s.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}
