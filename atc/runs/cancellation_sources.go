package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// CancellationSource reaches the original node without depending on Kubernetes
// in the Run service. Neither a missing Pod nor a replacement node is closure.
type CancellationSource interface {
	InterruptExecution(context.Context, string, executioncontrol.Acknowledgement) error
	RecoverExecutionOutcome(context.Context, string, executioncontrol.Acknowledgement) (executioncontrol.ClassifyResult, error)
	ClassifyExecution(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.ClassifyResult, error)
	StopExecution(context.Context, string, string, executioncontrol.ActivationEpoch, executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error)
	CaptureControl(context.Context, string, string, executioncontrol.ActivationEpoch) (hangaroutput.SourceControl, error)
}

// CancellationSources connects durable Run work to exact execution control and
// the existing generic capture coordinator. It owns no execution or retry store.
type CancellationSources struct {
	Conn        db.DbConn
	Factory     db.PipelineRunFactory
	Repository  *db.RunOutputRepository
	Source      CancellationSource
	Coordinator *hangaroutput.Coordinator
	Verifier    db.RunExecutionVerifier
}

// CancellationActionSet composes owners without silently completing a kind that
// none implements. Each handler must refuse unsupported kinds before any work.
type CancellationActionSet []CancellationActions

type CancellationActionFunc func(context.Context, db.RunCancellationLease, db.RunCancellationOperation) (db.RunCancellationDebt, error)

func (action CancellationActionFunc) ExecuteCancellationOperation(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
	return action(ctx, lease, op)
}

func (actions CancellationActionSet) ExecuteCancellationOperation(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
	for _, action := range actions {
		debt, err := action.ExecuteCancellationOperation(ctx, lease, op)
		if !errors.Is(err, db.ErrRunCancellationExternalWork) {
			return debt, err
		}
	}
	return db.CancellationUnavailable, db.ErrRunCancellationExternalWork
}

func (s *CancellationSources) ExecuteCancellationOperation(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation) (db.RunCancellationDebt, error) {
	switch op.Kind {
	case db.CancelHandoff, db.CancelExecution, db.CancelSourceHold, db.CancelCapture:
	default:
		return db.CancellationUnavailable, db.ErrRunCancellationExternalWork
	}
	if s.Conn == nil || s.Factory == nil || s.Repository == nil || s.Source == nil || s.Coordinator == nil {
		return db.CancellationUnavailable, fmt.Errorf("incomplete cancellation source handler")
	}
	var in db.RunCancellationSource
	err := s.transaction(ctx, func(tx db.Tx) error {
		var err error
		in, err = s.Factory.CancellationOutputTask(ctx, tx, lease, op)
		return err
	})
	if err != nil {
		return cancellationSourceDebt(err), err
	}
	r := in.Task.Record
	if !r.Source.Reserved() && in.Dispatched {
		// A dispatched reservation with no recorded reply is unresolved, not absent.
		// OutputStarter recovers the same admission; cleanup never creates another.
		return db.CancellationPending, nil
	}
	switch op.Kind {
	case db.CancelHandoff:
		if in.Classified {
			return db.CancellationDone, nil
		}
		if !r.Source.Reserved() {
			return s.settle(ctx, lease, op, in)
		}
		err = s.classify(ctx, lease, op, in)
	case db.CancelExecution:
		if !r.Source.Reserved() {
			return db.CancellationDone, nil
		}
		if !in.Classified {
			return db.CancellationPending, nil
		}
		err = s.closeExecution(ctx, lease, op, in)
	case db.CancelSourceHold, db.CancelCapture:
		if r.Source.Reserved() && !in.Classified {
			return db.CancellationPending, nil
		}
		return s.settle(ctx, lease, op, in)
	}
	if errors.Is(err, atc.ErrRunOutputPending) {
		return db.CancellationPending, nil
	}
	return cancellationSourceDebt(err), err
}

func (s *CancellationSources) observation(ctx context.Context, in db.RunCancellationSource) (db.RunOutputCancellationEvidence, error) {
	t := in.Task
	classified, err := s.Source.ClassifyExecution(ctx, t.NodeName, t.NodeUID, t.Record.ActivationEpoch, t.Record.Execution)
	if err != nil {
		return db.RunOutputCancellationEvidence{}, err
	}
	if err = classified.Validate(); err != nil {
		return db.RunOutputCancellationEvidence{}, err
	}
	if classified.Identity != t.Record.Execution {
		return db.RunOutputCancellationEvidence{}, output.ErrInvalidIdentity
	}
	classified, err = recoverCancellationOutcome(ctx, s.Source, s.Verifier, t.NodeName, in.Start, classified)
	if err != nil {
		return db.RunOutputCancellationEvidence{}, err
	}
	return db.RunOutputCancellationEvidence{NodeUID: t.NodeUID, Execution: classified}, nil
}

func (s *CancellationSources) classify(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation, in db.RunCancellationSource) error {
	evidence, err := s.observation(ctx, in)
	if err != nil {
		return err
	}
	var hold *output.CaptureAcknowledgement
	if !in.Task.Record.HoldAcknowledged {
		t := in.Task
		control, err := s.Source.CaptureControl(ctx, t.NodeName, t.NodeUID, t.Record.ActivationEpoch)
		if err != nil {
			return err
		}
		observed, err := control.InspectHold(ctx, t.Record.Execution, t.Record.HandoffID)
		if err == nil {
			hold = &observed
		} else if !errors.Is(err, output.ErrNotFound) {
			return err
		} else if evidence.Execution.Classification != executioncontrol.ClassificationNeverStarted {
			return atc.ErrRunOutputPending
		}
	}
	return s.transaction(ctx, func(tx db.Tx) error {
		if hold != nil {
			if err := s.Repository.AcknowledgeSourceHold(ctx, tx, *hold); err != nil {
				return err
			}
		}
		if err := s.Repository.RecordCancellationClassification(ctx, tx, lease, in.Task.Record.HandoffID, evidence); err != nil {
			return err
		}
		return s.Factory.CheckCancellationOperation(ctx, tx, lease, op)
	})
}

func (s *CancellationSources) closeExecution(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation, in db.RunCancellationSource) error {
	evidence, err := s.observation(ctx, in)
	if err != nil {
		return err
	}
	switch evidence.Execution.Classification {
	case executioncontrol.ClassificationNeverStarted, executioncontrol.ClassificationExecuting:
		t := in.Task
		closure, err := s.Source.StopExecution(ctx, t.NodeName, t.NodeUID, t.Record.ActivationEpoch, t.Record.Execution)
		if err != nil {
			return err
		}
		if err = closure.Validate(); err != nil {
			return err
		}
		if closure.Identity != t.Record.Execution {
			return output.ErrInvalidIdentity
		}
		if !closure.Accepted {
			return atc.ErrRunOutputPending
		}
		if evidence.Execution.Classification == executioncontrol.ClassificationExecuting && in.Start != nil {
			if err := s.Source.InterruptExecution(ctx, t.NodeName, *in.Start); err != nil {
				if errors.Is(err, output.ErrUnresolved) {
					return atc.ErrRunOutputPending
				}
				return err
			}
		}
		evidence, err = s.observation(ctx, in)
		if err != nil {
			return err
		}
		if evidence.Execution.Classification == executioncontrol.ClassificationNeverStarted {
			evidence.StartClosure = &closure
		}
	}
	// A stop request is not its completion. An executing or unresolved node
	// response is deliberately left pending by the evidence repository.
	return s.transaction(ctx, func(tx db.Tx) error {
		if err := s.Repository.RecordCancellationEvidence(ctx, tx, lease, in.Task.Record.HandoffID, evidence); err != nil {
			return err
		}
		return s.Factory.CheckCancellationOperation(ctx, tx, lease, op)
	})
}

func (s *CancellationSources) settle(ctx context.Context, lease db.RunCancellationLease, op db.RunCancellationOperation, in db.RunCancellationSource) (db.RunCancellationDebt, error) {
	// The shared coordinator stays stateless. Only this invocation receives the
	// exact locator and a transaction opener bounded by its operation deadline.
	coordinator := *s.Coordinator
	coordinator.Repository = s.Repository
	coordinator.Transactor = cancellationTransactor{ctx: ctx, sources: s, lease: lease, op: op}
	coordinator.Dialer = hangaroutput.SourceDialerFunc(func(locator string) (hangaroutput.SourceControl, error) {
		t := in.Task
		if locator != t.NodeName {
			return nil, output.ErrInvalidIdentity
		}
		return s.Source.CaptureControl(ctx, t.NodeName, t.NodeUID, t.Record.ActivationEpoch)
	})
	_, err := coordinator.Cancel(ctx, in.Task.Record.HandoffID)
	if errors.Is(err, atc.ErrRunOutputPending) {
		return db.CancellationPending, nil
	}
	if err != nil {
		return cancellationSourceDebt(err), err
	}
	var status output.HandoffStatus
	err = s.transaction(ctx, func(tx db.Tx) error {
		var err error
		status, err = s.Repository.ClassifyHandoff(ctx, tx, in.Task.Record.HandoffID)
		if err != nil {
			return err
		}
		return s.Factory.CheckCancellationOperation(ctx, tx, lease, op)
	})
	if err != nil {
		return cancellationSourceDebt(err), err
	}
	if !status.Settled {
		return db.CancellationPending, nil
	}
	return db.CancellationDone, nil
}

func cancellationSourceDebt(err error) db.RunCancellationDebt {
	switch {
	case err == nil:
		return db.CancellationDone
	case errors.Is(err, atc.ErrRunOutputPending):
		return db.CancellationPending
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return db.CancellationTimeout
	case errors.Is(err, db.ErrRunCancellationProgressStale), errors.Is(err, db.ErrRunCancellationLeaseLost), errors.Is(err, output.ErrConflict), errors.Is(err, output.ErrInvalidIdentity):
		return db.CancellationConflict
	default:
		return db.CancellationUnavailable
	}
}

func (s *CancellationSources) transaction(ctx context.Context, fn func(db.Tx) error) error {
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

type cancellationTransactor struct {
	ctx     context.Context
	sources *CancellationSources
	lease   db.RunCancellationLease
	op      db.RunCancellationOperation
}

func (t cancellationTransactor) Begin() (hangaroutput.Transaction, error) {
	tx, err := t.sources.Conn.BeginTx(t.ctx, nil)
	if err != nil {
		return nil, err
	}
	return db.HangarOutputTx{Tx: &cancellationTransaction{Tx: tx, owner: t}}, nil
}

type cancellationTransaction struct {
	db.Tx
	owner cancellationTransactor
}

func (tx *cancellationTransaction) Commit() error {
	t := tx.owner
	if err := t.sources.Factory.CheckCancellationOperation(t.ctx, tx.Tx, t.lease, t.op); err != nil {
		return err
	}
	return tx.Tx.Commit()
}
