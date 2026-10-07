package hangaroutput

// What the coordinator needs from the world, declared where it is CONSUMED.
//
// PostgreSQL implements the capture rows in atc/db; the node half is HTTP to a
// daemon. Every name here is product-neutral: there is no build, job, check,
// Run, workflow, ticket, agent or playbook, and the one deployment concept
// that reaches this package -- which node holds a step directory -- arrives as
// a node name and UID that are handed straight back to the dialer.

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Transaction is the caller's transaction, plus the two things only its owner
// may do. The row methods take output.Tx, which is deliberately ExecContext and
// QueryContext and nothing that could commit.
type Transaction interface {
	output.Tx

	Commit() error
	Rollback() error
}

// Transactor begins one.
type Transactor interface {
	Begin() (Transaction, error)
}

// CaptureRows is the capture row: the only durable capture state there is.
// Every write is a compare-and-set; a replay of a commit whose answer was lost
// succeeds, and anything else is output.ErrConflict.
type CaptureRows interface {
	InsertPending(ctx context.Context, tx output.Tx, pending output.PendingCapture) (output.Capture, error)
	GetCapture(ctx context.Context, tx output.Tx, key output.CaptureKey) (output.Capture, error)
	CASPendingToPublishing(ctx context.Context, tx output.Tx, key output.CaptureKey,
		pod executioncontrol.PodUID, scope hangar.Scope, digest hangar.Digest) (output.Capture, error)
	CASPendingToDiscarded(ctx context.Context, tx output.Tx, key output.CaptureKey, reason string) (output.Capture, error)
	CASPublishingToPublished(ctx context.Context, tx output.Tx, published output.PublishedCapture) (output.Capture, error)
	MarkFailed(ctx context.Context, tx output.Tx, key output.CaptureKey, reason string) (output.Capture, error)
	SetReleased(ctx context.Context, tx output.Tx, key output.CaptureKey) (output.Capture, error)

	ListPending(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error)
	ListPendingPastDeadline(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error)
	ListPublishingForRecovery(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error)
	ListPublishingPastDeadline(ctx context.Context, tx output.Tx, margin time.Duration, limit int) ([]output.Capture, error)
	ListUnreleased(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error)
}

// SourceControl is one node daemon: the base protocol's observation of an
// execution, and the capture routes over its step directories.
type SourceControl interface {
	output.SourceControl

	Observe(ctx context.Context, id executioncontrol.Identity,
		wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error)
}

// SourceDialer reaches the daemon on one node. A node replaced under the same
// name is a different node and is refused by the dialer.
type SourceDialer interface {
	ForNode(ctx context.Context, name string, uid executioncontrol.NodeUID) (SourceControl, error)
}

// SourceDialerFunc adapts a function to the SourceDialer port.
type SourceDialerFunc func(context.Context, string, executioncontrol.NodeUID) (SourceControl, error)

func (dial SourceDialerFunc) ForNode(ctx context.Context, name string, uid executioncontrol.NodeUID) (SourceControl, error) {
	return dial(ctx, name, uid)
}

type unavailableDialer struct{}

// NoSourcePlane is the dialer of a deployment with no output plane: every
// node is unreachable, so nothing advances and nothing is guessed.
func NoSourcePlane() SourceDialer { return unavailableDialer{} }

func (unavailableDialer) ForNode(_ context.Context, name string, _ executioncontrol.NodeUID) (SourceControl, error) {
	return nil, fmt.Errorf("%w: this deployment has no output plane, and step directories on %q "+
		"cannot be reached", output.ErrInfrastructure, name)
}
