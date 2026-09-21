package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/output"
)

// OutputSource is the node plane. Every call occurs outside a database
// transaction; it cannot select or alter a Run's producer identity.
type OutputSource interface {
	SelectNode(context.Context, runtime.ContainerSpec) (name, uid string, err error)
	ReserveSource(context.Context, string, string, output.CaptureAdmission) (output.ReservedIncarnation, error)
	RuntimeControl(context.Context, string, output.HandoffRecord) (*runtime.ExecutionControl, error)
}

// OutputStarter admits the exact source before a producing Pod can be built.
// Its recovery operation is also driven by the existing capture component.
type OutputStarter struct {
	conn    db.DbConn
	factory db.PipelineRunFactory
	source  OutputSource
	epoch   int64
	term    time.Duration
}

func NewOutputStarter(conn db.DbConn, factory db.PipelineRunFactory, source OutputSource, epoch int64, term time.Duration) *OutputStarter {
	return &OutputStarter{conn: conn, factory: factory, source: source, epoch: epoch, term: term}
}

func (s *OutputStarter) Prepare(ctx context.Context, buildID int, plan atc.TaskPlan, spec runtime.ContainerSpec) (*runtime.ExecutionControl, error) {
	if s.source == nil || plan.RunResult == nil || plan.TaskID == "" {
		return nil, atc.ErrRunResultsUnavailable
	}
	if err := runtime.ValidateCaptureOutput(spec, plan.RunResult.Output); err != nil {
		return nil, err
	}
	var in db.RunOutputTask
	var found bool
	err := s.transaction(ctx, func(tx db.Tx) (err error) {
		in, found, err = s.factory.OutputTask(ctx, tx, buildID, plan.TaskID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !found {
		in.NodeName, in.NodeUID, err = s.source.SelectNode(ctx, spec)
		if err != nil {
			return nil, err
		}
	}
	in.BuildID, in.Plan = buildID, plan
	err = s.transaction(ctx, func(tx db.Tx) (err error) {
		in.Record, err = s.factory.PredeclareOutputTask(ctx, tx, buildID, plan, s.epoch, s.term, in.NodeName, in.NodeUID)
		if err != nil {
			return err
		}
		return s.factory.RequestOutputSource(ctx, tx, buildID, plan, s.epoch)
	})
	if err != nil {
		return nil, err
	}
	if !in.Record.Source.Reserved() {
		if err := s.reserve(ctx, in); err != nil {
			return nil, err
		}
	}
	// Dispatch may have raced an abort. Recheck admission before returning any
	// runtime authority; reconciliation after abort permits cleanup only.
	err = s.transaction(ctx, func(tx db.Tx) (err error) {
		in.Record, err = s.factory.PredeclareOutputTask(ctx, tx, buildID, plan, s.epoch, s.term, in.NodeName, in.NodeUID)
		return err
	})
	if err != nil {
		return nil, err
	}
	control, err := s.source.RuntimeControl(ctx, in.NodeName, in.Record)
	if err != nil {
		return nil, err
	}
	if control == nil {
		return nil, atc.ErrRunResultsUnavailable
	}
	if err := control.Validate(spec); err != nil {
		return nil, err
	}
	return control, nil
}

// Run reconciles a bounded batch of admitted dispatches. It reissues the exact
// reservation, including when the original controller lost the daemon's reply.
// It never builds a Pod or grants execution authority.
func (s *OutputStarter) Run(ctx context.Context) error {
	if s.source == nil {
		return atc.ErrRunResultsUnavailable
	}
	var pending []db.RunOutputTask
	err := s.transaction(ctx, func(tx db.Tx) (err error) {
		pending, err = s.factory.PendingOutputSources(ctx, tx, 100)
		return err
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, in := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		if err := s.reserve(ctx, in); err != nil {
			errs = append(errs, fmt.Errorf("Run source %s: %w", in.Record.HandoffID, err))
		}
	}
	return errors.Join(errs...)
}

func (s *OutputStarter) reserve(ctx context.Context, in db.RunOutputTask) error {
	r := in.Record
	reserved, err := s.source.ReserveSource(ctx, in.NodeName, in.NodeUID, output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: r.ActivationEpoch,
		HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Output: r.Output, CaptureDeadline: r.CaptureDeadline,
	})
	if err != nil {
		return err
	}
	return s.transaction(ctx, func(tx db.Tx) error {
		return s.factory.RecordOutputSource(ctx, tx, in.BuildID, in.Plan, reserved, in.NodeName)
	})
}

func (s *OutputStarter) transaction(ctx context.Context, f func(db.Tx) error) error {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := f(tx); err != nil {
		return err
	}
	return tx.Commit()
}
