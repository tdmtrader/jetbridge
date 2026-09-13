package hangaroutput

// The component: bounded work over whatever is incomplete.
//
// It exists because the process that started a capture is exactly the process
// that may be gone. A capture crosses two systems and an ATC restart, and what
// has to survive is not a goroutine but the ability to ask what is durably true
// and take the next bounded step.
//
// It advances ONE transition per handoff per pass, and it takes a bounded
// batch. Both are the same rule: a component that drove one capture to
// completion before looking at the next would hold every other capture behind
// an unreachable node's timeout.

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/hangar/output"
)

// DefaultBatchSize bounds one pass. It is a batch and not a limit on captures:
// an unfinished handoff is picked up again on the next notification or the
// periodic fallback.
const DefaultBatchSize = 100

// IncompleteReader lists the handoffs that still owe something.
//
// It is the component's only query, and it is a query about DEBT rather than
// about age or state: what a coordinator has to look at is exactly the set that
// has not settled, and a list built from anything else would either miss one or
// keep visiting captures that are done.
type IncompleteReader interface {
	IncompleteHandoffs(ctx context.Context, tx output.Tx, limit int) ([]output.HandoffID, error)
}

// DebtReporter receives what the plane still owes, after every pass.
//
// The shape is the whole point: a COUNT per bounded label, and no identity
// anywhere. A metric keyed by handoff would be a metrics store with one series
// per capture forever, which is the cardinality failure this plane is otherwise
// careful to avoid -- and it would put an opaque id, which a consumer may treat
// as sensitive, into a system nobody thinks of as a log.
//
// `state` is the transition a handoff is waiting on, which is a member of a
// closed set. It answers the two questions an operator has: how much is
// outstanding, and is any of it stuck somewhere it should not be.
type DebtReporter interface {
	ReportDebt(ctx context.Context, byTransition map[Transition]int)
}

// Recoverer is the component.
type Recoverer struct {
	Coordinator *Coordinator
	Incomplete  IncompleteReader
	Transactor  Transactor
	Debt        DebtReporter
	BatchSize   int
}

func (recoverer *Recoverer) batchSize() int {
	if recoverer.BatchSize <= 0 {
		return DefaultBatchSize
	}

	return recoverer.BatchSize
}

// Run advances every incomplete handoff by at most one transition.
//
// A failure on one handoff is LOGGED and does not stop the pass. That is not
// tolerance for bugs: the commonest failure here is a node that cannot be
// reached, and one unreachable node must not stop every other capture in the
// deployment from settling.
func (recoverer *Recoverer) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx)

	tx, err := recoverer.Transactor.Begin()
	if err != nil {
		return err
	}
	handoffs, err := recoverer.Incomplete.IncompleteHandoffs(ctx, tx, recoverer.batchSize())
	_ = tx.Rollback()
	if err != nil {
		return err
	}

	debt := map[Transition]int{}
	for _, handoff := range handoffs {
		decision, err := recoverer.Coordinator.Advance(ctx, handoff)
		debt[decision.Transition]++
		if err == nil {
			continue
		}

		// The redaction rule, at the one place this component says anything.
		// A handoff id, a transition and a class -- never a grant, a key, a
		// path or a consumer reference, and never the error's own text, which
		// is where a daemon's message about a directory would arrive.
		logger.Info("capture-transition-failed", lager.Data{
			"handoff":    string(handoff),
			"transition": string(decision.Transition),
			"class":      classOf(err),
		})
	}

	if recoverer.Debt != nil {
		recoverer.Debt.ReportDebt(ctx, debt)
	}

	return nil
}

// classOf reduces an error to a bounded word.
//
// Bounded because it is a metric label and a log field, and an unbounded one is
// a cardinality problem in a metrics store and a leak in a log. The leaf's
// sentinels are the vocabulary; anything else is `other`, which is honest --
// it says a class this plane does not name, rather than inventing one.
func classOf(err error) string {
	for _, candidate := range []struct {
		sentinel error
		name     string
	}{
		{output.ErrUnauthorized, "unauthorized"},
		{output.ErrConflict, "conflict"},
		{output.ErrNotFound, "not_found"},
		{output.ErrSealed, "sealed"},
		{output.ErrSealUnconfirmed, "seal_unconfirmed"},
		{output.ErrUnresolved, "unresolved"},
		{output.ErrIncomplete, "incomplete"},
		{output.ErrInvalidIdentity, "invalid_identity"},
		{output.ErrCorrupt, "corrupt"},
		{output.ErrInfrastructure, "unavailable"},
	} {
		if errors.Is(err, candidate.sentinel) {
			return candidate.name
		}
	}

	return "other"
}

// Name and Reload make a Recoverer satisfy the component contract without the
// caller needing to know this package's shape.
func (recoverer *Recoverer) String() string {
	return fmt.Sprintf("hangar output capture recoverer, batches of %d", recoverer.batchSize())
}
