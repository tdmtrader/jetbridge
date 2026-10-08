package runs

import (
	"context"
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
	RuntimeControl(context.Context, output.Capture) (*runtime.ExecutionControl, error)
}

// OutputStarter is step 1 of a Run producer's capture: before the producing
// Pod can be built, a pending capture row on a selected node, and the
// execution envelope whose control init will write that node's held marker.
type OutputStarter struct {
	conn    db.DbConn
	factory db.PipelineRunFactory
	source  OutputSource
	term    time.Duration
}

func NewOutputStarter(conn db.DbConn, factory db.PipelineRunFactory, source OutputSource, term time.Duration) *OutputStarter {
	return &OutputStarter{conn: conn, factory: factory, source: source, term: term}
}

func (s *OutputStarter) Prepare(ctx context.Context, buildID int, plan atc.TaskPlan, spec runtime.ContainerSpec) (*runtime.ExecutionControl, error) {
	if s.source == nil || plan.RunResult == nil || plan.TaskID == "" {
		return nil, atc.ErrRunResultsUnavailable
	}
	if err := runtime.ValidateCaptureOutput(spec, plan.RunResult.Output); err != nil {
		return nil, err
	}
	var existing db.RunCapture
	var found bool
	err := s.transaction(ctx, func(tx db.Tx) (err error) {
		existing, found, err = s.factory.RunCaptureTask(ctx, tx, buildID, plan.TaskID)
		return err
	})
	if err != nil {
		return nil, err
	}
	node, uid := existing.Capture.Node, string(existing.Capture.NodeUID)
	if !found {
		node, uid, err = s.source.SelectNode(ctx, spec)
		if err != nil {
			return nil, err
		}
	}
	// A replay returns the original capture; a cancellation or abort that
	// raced this start refuses it here, under the Run lock.
	var started db.RunCapture
	err = s.transaction(ctx, func(tx db.Tx) (err error) {
		started, err = s.factory.StartRunCapture(ctx, tx, buildID, plan, s.term, node, uid)
		return err
	})
	if err != nil {
		return nil, err
	}
	control, err := s.source.RuntimeControl(ctx, started.Capture)
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

func (s *OutputStarter) transaction(ctx context.Context, f func(db.Tx) error) error {
	return inTransaction(ctx, s.conn, f)
}
