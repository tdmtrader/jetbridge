// Package controller is the shared scaffolding the three isolated output-plane
// controllers run on: a durable per-kind operation lease, a bounded unit of
// work, and a nonzero periodic wake beside whatever notification accelerates it.
//
// It names no object store and has no GCS method. That is the point rather than
// an omission: a Kubernetes service account is Pod-wide, so the three
// controllers are three binaries with three identities, and the code they share
// must be the code that needs none of them. Each binary links exactly one role
// package and wires it into a Runner declared here.
//
// Everything is measured on the DATABASE clock. A controller is a process that
// can be paused, restarted, partitioned or replaced, and a lease measured on
// such a process's own clock expires when the process decides it has.
package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/hangar/output"
)

// Transaction is the caller's transaction plus the two things only its owner
// may do. The repository methods take output.Tx, which is deliberately
// ExecContext and QueryContext and nothing that could commit.
type Transaction interface {
	output.Tx

	Commit() error
	Rollback() error
}

// Transactor begins one.
type Transactor interface {
	Begin() (Transaction, error)
}

// Leases is the durable ownership half, declared where it is CONSUMED.
//
// atc/db implements it. This package names no database package: an interface
// here is a statement of what a controller uses, and the package that satisfies
// one does not have to know it exists.
type Leases interface {
	ClaimOperationLease(ctx context.Context, tx output.Tx, kind output.OperationKind, epoch int64, owner string, term time.Duration) (output.OperationLease, error)
	RenewOperationLease(ctx context.Context, tx output.Tx, lease output.OperationLease, term time.Duration) (output.OperationLease, error)
}

// Pass is one bounded unit of a controller's work.
//
// It is handed the lease it runs under so it can name the fence on every write
// and stop when the lease is nearly gone. It returns how much it did, which is
// the only number the telemetry below needs: a controller that did nothing and
// a controller that is stuck look identical without it.
type Pass interface {
	Run(ctx context.Context, lease output.OperationLease) (int, error)
}

// PassFunc adapts a function to Pass.
type PassFunc func(ctx context.Context, lease output.OperationLease) (int, error)

func (fn PassFunc) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	return fn(ctx, lease)
}

// Reporter receives what one pass did, after every pass.
//
// A COUNT per bounded label and no identity anywhere. A metric keyed by object
// key or reservation id would be a metrics store with one series per capture
// forever -- the cardinality failure this plane is otherwise careful to avoid --
// and it would put an opaque id, which a consumer may treat as sensitive, into
// a system nobody thinks of as a log.
type Reporter interface {
	ReportPass(ctx context.Context, kind output.OperationKind, processed int, class string)
}

// ReporterFunc adapts a function to Reporter.
type ReporterFunc func(ctx context.Context, kind output.OperationKind, processed int, class string)

func (fn ReporterFunc) ReportPass(ctx context.Context, kind output.OperationKind, processed int, class string) {
	fn(ctx, kind, processed, class)
}

// Runner is one controller: one operation kind, one lease, one bounded pass.
type Runner struct {
	Kind            output.OperationKind
	ActivationEpoch int64
	OwnerID         string
	Term            time.Duration

	Transactor Transactor
	Leases     Leases
	Pass       Pass
	Reporter   Reporter

	// lease is what this runner currently believes it holds. It is kept across
	// passes so a renewal can name its fence, and it is cleared the moment a
	// claim or renewal refuses: a runner that kept a lease it was told it had
	// lost would be the stale owner every fence in this plane exists to stop.
	lease output.OperationLease
	held  bool
}

func (runner *Runner) term() time.Duration {
	if runner.Term <= 0 {
		return output.MinLeaseTerm
	}

	return runner.Term
}

// Run takes or renews the lease and performs one bounded pass.
//
// It is the shape component.Runner already drives: one call per wake, bounded,
// returning an error that is logged rather than one that stops the process. A
// controller that drove its whole backlog to completion before returning would
// hold every other kind behind an unreachable node's timeout -- which is the
// same rule the capture recoverer already follows, for the same reason.
//
// Losing the lease is NOT an error. One kind has one owner, and the pass that
// does not own it this minute simply does nothing this minute; reporting that
// as a failure would make a correctly configured pair of replicas look broken
// every time they raced.
func (runner *Runner) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx)

	lease, err := runner.hold(ctx)
	if err != nil {
		if errors.Is(err, output.ErrConflict) {
			runner.report(ctx, 0, "not_owner")

			return nil
		}
		runner.report(ctx, 0, classOf(err))

		return err
	}

	processed, err := runner.Pass.Run(ctx, lease)
	runner.report(ctx, processed, classOf(err))
	if err != nil {
		// The redaction rule, at the one place a controller says anything. A
		// kind, a count and a class -- never a key, a ref, a grant or a path,
		// and never the error's own text, which is where a store's message
		// about an object would arrive.
		logger.Info("hangar-output-pass-failed", lager.Data{
			"kind":      string(runner.Kind),
			"processed": processed,
			"class":     classOf(err),
		})
	}

	return nil
}

// hold claims the lease, or renews the one this runner already has.
func (runner *Runner) hold(ctx context.Context) (output.OperationLease, error) {
	if err := runner.Kind.Validate(); err != nil {
		return output.OperationLease{}, err
	}
	if runner.Transactor == nil || runner.Leases == nil || runner.Pass == nil {
		return output.OperationLease{}, fmt.Errorf("%w: the %s controller is missing a "+
			"transactor, a lease store or a pass", output.ErrIncomplete, runner.Kind)
	}

	tx, err := runner.Transactor.Begin()
	if err != nil {
		return output.OperationLease{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var lease output.OperationLease
	if runner.held {
		lease, err = runner.Leases.RenewOperationLease(ctx, tx, runner.lease, runner.term())
		if err != nil {
			// The renewal was refused, so this runner is not the owner any
			// more. Forget the lease before returning: the next pass claims
			// afresh rather than renewing under a fence somebody else advanced.
			runner.held = false
			runner.lease = output.OperationLease{}

			return output.OperationLease{}, err
		}
	} else {
		lease, err = runner.Leases.ClaimOperationLease(ctx, tx,
			runner.Kind, runner.ActivationEpoch, runner.OwnerID, runner.term())
		if err != nil {
			return output.OperationLease{}, err
		}
	}

	if err := tx.Commit(); err != nil {
		runner.held = false
		runner.lease = output.OperationLease{}

		return output.OperationLease{}, err
	}

	runner.lease = lease
	runner.held = true

	return lease, nil
}

func (runner *Runner) report(ctx context.Context, processed int, class string) {
	if runner.Reporter == nil {
		return
	}
	runner.Reporter.ReportPass(ctx, runner.Kind, processed, class)
}

// Deferred: Holds is read by the liveness specs and by the Phase 8 status
// surface; no running process decides anything from it, and a runner that
// decided from its own belief rather than from the lease would be the stale
// owner every fence in this plane exists to stop
//
// Holds reports whether this runner currently believes it owns its lease.
func (runner *Runner) Holds() bool { return runner.held }

func (runner *Runner) String() string {
	return fmt.Sprintf("hangar output %s controller", runner.Kind)
}

// classOf reduces an error to a bounded word.
//
// Bounded because it is a metric label and a log field, and an unbounded one is
// a cardinality problem in a metrics store and a leak in a log. The leaf's
// sentinels are the vocabulary; anything else is `other`, which is honest -- it
// says a class this plane does not name rather than inventing one.
func classOf(err error) string {
	if err == nil {
		return "ok"
	}
	for _, candidate := range []struct {
		sentinel error
		name     string
	}{
		{output.ErrUnauthorized, "unauthorized"},
		{output.ErrConflict, "conflict"},
		{output.ErrNotFound, "not_found"},
		{output.ErrGenerationConflict, "generation_conflict"},
		{output.ErrAtRisk, "at_risk"},
		{output.ErrTimeout, "timeout"},
		{output.ErrUnresolved, "unresolved"},
		{output.ErrIncomplete, "incomplete"},
		{output.ErrCorrupt, "corrupt"},
		{output.ErrLimitExceeded, "limit_exceeded"},
		{output.ErrInfrastructure, "unavailable"},
	} {
		if errors.Is(err, candidate.sentinel) {
			return candidate.name
		}
	}

	return "other"
}

// Interval is the periodic wake every worker in this plane must have.
//
// NOTIFY accelerates work; it is never the only way work is found, because a
// worker with a zero interval wakes only on a notification and a missed one
// would strand eligible work until a restart. The value is a function rather
// than a constant so that a caller cannot pass zero by forgetting a field:
// there is one spelling and it is nonzero by construction.
//
// The ceiling is the KIND's, not one number for all of them. Req 50 puts a
// one-minute fallback on capture recovery, inventory and reclaim -- the workers
// whose backlog is work waiting -- and Req 51 puts fifteen minutes on the
// policy attestation, whose bound is a detection window and whose every pass
// costs a whole-bucket lifecycle and IAM read. One ceiling for both meant the
// attestor's validated, configured, five-minute interval was silently clamped
// to sixty seconds and the deployment read IAM fifteen times more often than
// anyone asked: compliant with "at least every 15 minutes", and not what was
// configured or reviewed.
func Interval(kind output.OperationKind, configured time.Duration) time.Duration {
	ceiling := CeilingFor(kind)
	if configured <= 0 || configured > ceiling {
		return ceiling
	}

	return configured
}

// CeilingFor is the slowest periodic wake a kind may be configured with.
func CeilingFor(kind output.OperationKind) time.Duration {
	if kind == output.OperationPolicyAttestation {
		// The detection bound itself. The attestor refuses a configured
		// interval at or above it before a runner is built -- an attestation
		// replaced exactly at the bound is stale for an instant beforehand, and
		// admission is blocked for that instant every cycle -- so this ceiling
		// never clamps a value that reached it through the binary. It is here
		// for the caller that builds a Runner some other way, and it clamps to
		// the worst PERMISSIBLE wake rather than silently to sixty seconds.
		return output.MaxPolicyEvidenceAge
	}

	return output.WorkerFallbackInterval
}
