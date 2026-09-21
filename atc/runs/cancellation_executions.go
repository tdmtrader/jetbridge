package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

type CancellationExecutionSource interface {
	ExecutionStart(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.Acknowledgement, error)
	InterruptExecution(context.Context, string, executioncontrol.Acknowledgement) error
	RecoverExecutionOutcome(context.Context, string, executioncontrol.Acknowledgement) (executioncontrol.ClassifyResult, error)
	ClassifyExecution(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.ClassifyResult, error)
	StopExecution(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error)
	BaseRuntimeControl(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (*runtime.ExecutionControl, error)
}

type CancellationSourcePlane interface {
	CancellationSource
	CancellationExecutionSource
}

// CancellationExecutions reconciles commands without selected outputs through
// the same exact node protocol. An issued stop is never reported as completion.
type CancellationExecutions struct {
	Conn     db.DbConn
	Factory  db.PipelineRunFactory
	Source   CancellationExecutionSource
	Verifier db.RunExecutionVerifier
}

func (s *CancellationExecutions) ExecuteCancellationOperation(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
	if op.Kind != db.CancelExecution {
		return db.CancellationUnavailable, db.ErrRunCancellationExternalWork
	}
	if s.Conn == nil || s.Factory == nil || s.Source == nil || s.Verifier == nil {
		return db.CancellationUnavailable, fmt.Errorf("incomplete cancellation execution handler")
	}
	var in db.RunCancellationExecution
	err := s.transaction(ctx, func(tx db.Tx) error {
		var err error
		in, err = s.Factory.CancellationRunExecution(ctx, tx, lease, op)
		return err
	})
	if err != nil {
		return cancellationSourceDebt(err), err
	}
	if in.Closed {
		return db.CancellationDone, nil
	}
	a := in.Admission
	if in.Start == nil {
		// The node may have committed a start the Run never retained: its
		// database was unavailable when the answer came back, or cancellation
		// closed admission before a replay could retain it. Without the start
		// nothing can interrupt the command or close the execution, so it is
		// read from the node that signed it and retained under the Run's
		// publication lock -- a node fact, never new start authority.
		start, err := s.retainNodeStart(ctx, a)
		if err != nil {
			return cancellationSourceDebt(err), err
		}
		in.Start = start
	}
	observe := func() (db.RunOutputCancellationEvidence, error) {
		c, err := s.Source.ClassifyExecution(ctx, a.NodeName, a.NodeUID, executioncontrol.ActivationEpoch(a.Epoch), a.Identity)
		if err != nil {
			return db.RunOutputCancellationEvidence{}, err
		}
		if err = c.Validate(); err != nil {
			return db.RunOutputCancellationEvidence{}, err
		}
		if c.Identity != a.Identity {
			return db.RunOutputCancellationEvidence{}, output.ErrInvalidIdentity
		}
		c, err = recoverCancellationOutcome(ctx, s.Source, s.Verifier, a.NodeName, in.Start, c)
		if err != nil {
			return db.RunOutputCancellationEvidence{}, err
		}
		return db.RunOutputCancellationEvidence{NodeUID: a.NodeUID, Execution: c}, nil
	}
	evidence, err := observe()
	if err != nil {
		return cancellationSourceDebt(err), err
	}
	if evidence.Execution.Classification == executioncontrol.ClassificationNeverStarted {
		// Admission could have committed in PostgreSQL before its first daemon
		// call. Reconcile that same identity before installing its stop fence.
		if _, err = s.Source.BaseRuntimeControl(ctx, a.NodeName, a.NodeUID, executioncontrol.ActivationEpoch(a.Epoch), a.Identity); err != nil {
			return cancellationSourceDebt(err), err
		}
	}
	switch evidence.Execution.Classification {
	case executioncontrol.ClassificationNeverStarted, executioncontrol.ClassificationExecuting:
		closure, err := s.Source.StopExecution(ctx, a.NodeName, a.NodeUID, executioncontrol.ActivationEpoch(a.Epoch), a.Identity)
		if err != nil {
			return cancellationSourceDebt(err), err
		}
		if err = closure.Validate(); err != nil {
			return cancellationSourceDebt(err), err
		}
		if closure.Identity != a.Identity {
			return db.CancellationConflict, output.ErrInvalidIdentity
		}
		if evidence.Execution.Classification == executioncontrol.ClassificationExecuting && in.Start != nil {
			if err := s.Source.InterruptExecution(ctx, a.NodeName, *in.Start); err != nil {
				if errors.Is(err, output.ErrUnresolved) {
					return db.CancellationPending, nil
				}
				return cancellationSourceDebt(err), err
			}
		}
		evidence, err = observe()
		if err != nil {
			return cancellationSourceDebt(err), err
		}
		if evidence.Execution.Classification == executioncontrol.ClassificationNeverStarted {
			evidence.StartClosure = &closure
		}
	}
	err = s.transaction(ctx, func(tx db.Tx) error {
		return s.Factory.RecordCancelledRunExecution(ctx, tx, lease, op, evidence, s.Verifier)
	})
	if errors.Is(err, atc.ErrRunOutputPending) {
		return db.CancellationPending, nil
	}
	return cancellationSourceDebt(err), err
}

// retainNodeStart reads the node's signed start for the admitted execution and
// retains it in the Run. A node that recorded no start answers not found, and
// the execution is then never-started as far as this node knows.
func (s *CancellationExecutions) retainNodeStart(ctx context.Context, a db.RunExecutionAdmission) (*executioncontrol.Acknowledgement, error) {
	start, err := s.Source.ExecutionStart(ctx, a.NodeName, a.NodeUID, executioncontrol.ActivationEpoch(a.Epoch), a.Identity)
	if errors.Is(err, output.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = s.Verifier.VerifyExecution(start); err != nil {
		return nil, err
	}
	if err = s.transaction(ctx, func(tx db.Tx) error {
		return s.Factory.RecordRunExecutionWitness(ctx, tx, a.BuildID, a.PlanID, start, s.Verifier)
	}); err != nil {
		return nil, err
	}
	return &start, nil
}

type executionOutcomeRecovery interface {
	RecoverExecutionOutcome(context.Context, string, executioncontrol.Acknowledgement) (executioncontrol.ClassifyResult, error)
}

func recoverCancellationOutcome(ctx context.Context, source executionOutcomeRecovery, verifier db.RunExecutionVerifier, node string, start *executioncontrol.Acknowledgement, current executioncontrol.ClassifyResult) (executioncontrol.ClassifyResult, error) {
	if current.Classification != executioncontrol.ClassificationExecuting || start == nil {
		return current, nil
	}
	if verifier == nil {
		return current, output.ErrIncomplete
	}
	if err := verifier.VerifyExecution(*start); err != nil {
		return current, err
	}
	if start.Identity != current.Identity || start.Kind != executioncontrol.AcknowledgementStart {
		return current, output.ErrInvalidIdentity
	}
	recovered, err := source.RecoverExecutionOutcome(ctx, node, *start)
	if errors.Is(err, output.ErrUnresolved) {
		return current, nil
	}
	if err != nil {
		return current, err
	}
	if err = recovered.Validate(); err != nil {
		return current, err
	}
	if recovered.Identity != current.Identity {
		return current, output.ErrInvalidIdentity
	}
	return recovered, nil
}

func (s *CancellationExecutions) transaction(ctx context.Context, fn func(db.Tx) error) error {
	tx, err := s.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
