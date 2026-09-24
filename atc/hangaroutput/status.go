package hangaroutput

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/concourse/concourse/hangar/output"
)

// StatusReader is the operator's view of the output plane, read in one pass.
//
// Requirement 52 says the plane "alerts operators" when it enters at-risk, and
// requirement 53 says its status surfaces state the residual trust boundary.
// Until this existed the plane emitted no metric at all: every fact below was
// in the database, readable by anyone who knew the schema, and invisible to
// everyone who did not. A deletion plane that has gone fail-closed and says so
// only in a table is one that stays fail-closed over a weekend.
//
// One pass and one transaction, deliberately. These numbers are compared with
// each other -- an at-risk epoch with no open violation is a different problem
// from one with six, and a cursor that has not moved while debt is climbing is
// a different problem from one that has not moved at all -- so reading them at
// six different instants would let an operator draw a conclusion about a state
// that never existed.
//
// It DECIDES nothing. Every predicate it calls is one the enforcing path calls
// too, and the enforcement lives there: this type asks the same questions and
// publishes the answers. A status surface that made its own judgement would be
// a second opinion, and the one that is not the enforcement is the one that
// drifts.
type StatusReader struct {
	Transactor Transactor
	Repository StatusStore

	// Epoch is the activation epoch this deployment speaks for.
	Epoch int64

	// Bucket is the output bucket's fingerprint, which is how a cursor and its
	// debt are keyed.
	Bucket string

	// DebtLimit bounds the debt read. The status surface reports HOW MUCH debt
	// there is, and a surface that read an unbounded backlog to count it would
	// be the thing that fell over when the backlog was the problem.
	DebtLimit int
}

// StatusStore is the read surface a status pass needs.
//
// Reads only. A status surface with a write in its port is one an operator can
// be persuaded to "just clear", and reconciliation of a runtime finding is
// a deliberate operator act with its own record.
type StatusStore interface {
	HangarDatabaseNow(ctx context.Context, tx output.Tx) (output.Timestamp, error)
	OpenPolicyViolations(ctx context.Context, tx output.Tx, epoch int64) ([]output.PolicyFinding, error)
	ReadInventoryCursorProgress(ctx context.Context, tx output.Tx, bucket string, epoch int64) (output.InventoryCursor, error)
	ReadInventoryDebt(ctx context.Context, tx output.Tx, bucket string, epoch int64, limit int) ([]output.InventoryDebt, error)
	ReadOperationLease(ctx context.Context, tx output.Tx, kind output.OperationKind, epoch int64) (output.OperationLease, error)
	CountOutputPlaneState(ctx context.Context, tx output.Tx, epoch int64) (output.PlaneCounts, error)
}

// Status is what one pass found.
type Status struct {
	// At is the DATABASE's clock, not this process's. Every age below is
	// derived from it, so a web node whose clock has drifted reports an age the
	// plane itself would agree with.
	At output.Timestamp

	// AtRisk is the fail-closed state, and Why is the open violation classes
	// that put it there. An at-risk plane with no reason is a bug in this
	// reader; an at-risk plane with six reasons is a different morning from one
	// with one.
	AtRisk bool
	Why    []string

	// Violations counts open, unreconciled violations by class.
	Violations map[output.PolicyViolation]int

	// Cycle and AtCycleStart are the inventory sweep's progress. A cursor that
	// is at the start of a cycle has either just wrapped or never moved, and
	// the cycle counter is what tells those apart.
	Cycle        int64
	AtCycleStart bool

	// Debt is unresolved inventory debt by reason, and DebtTruncated says the
	// read hit its limit -- which is itself the signal, because a backlog
	// larger than the limit is one nothing is draining.
	Debt          map[output.DebtReason]int
	DebtTruncated bool

	// Leases is the remaining term of each operation lease, by kind. A negative
	// remaining is an expired owner, which is exactly the state a takeover is
	// about to resolve -- or the state nothing is resolving.
	Leases map[output.OperationKind]time.Duration

	// Counts are the plane's inventories: live generations, open claims, open
	// read leases, unfinalized reclaim jobs and nonterminal captures.
	Counts output.PlaneCounts
}

// Read takes one pass.
func (reader *StatusReader) Read(ctx context.Context) (Status, error) {
	if err := reader.wired(); err != nil {
		return Status{}, err
	}

	tx, err := reader.Transactor.Begin()
	if err != nil {
		return Status{}, err
	}
	defer func() { _ = tx.Rollback() }()

	status := Status{
		Violations: map[output.PolicyViolation]int{},
		Debt:       map[output.DebtReason]int{},
		Leases:     map[output.OperationKind]time.Duration{},
	}

	status.At, err = reader.Repository.HangarDatabaseNow(ctx, tx)
	if err != nil {
		return Status{}, err
	}

	violations, err := reader.Repository.OpenPolicyViolations(ctx, tx, reader.Epoch)
	if err != nil {
		return Status{}, err
	}
	reasons := map[string]bool{}
	for _, finding := range violations {
		status.Violations[finding.Violation]++
		reasons[string(finding.Violation)] = true
	}

	status.AtRisk = len(reasons) != 0
	for reason := range reasons {
		status.Why = append(status.Why, reason)
	}
	sort.Strings(status.Why)

	cursor, err := reader.Repository.ReadInventoryCursorProgress(ctx, tx, reader.Bucket, reader.Epoch)
	if err != nil {
		return Status{}, err
	}
	status.Cycle = int64(cursor.Cycle)
	status.AtCycleStart = cursor.AtCycleStart()

	limit := reader.DebtLimit
	if limit <= 0 {
		limit = DefaultStatusDebtLimit
	}
	debt, err := reader.Repository.ReadInventoryDebt(ctx, tx, reader.Bucket, reader.Epoch, limit)
	if err != nil {
		return Status{}, err
	}
	for _, one := range debt {
		status.Debt[one.Reason]++
	}
	status.DebtTruncated = len(debt) >= limit

	for _, kind := range output.OperationKinds() {
		lease, err := reader.Repository.ReadOperationLease(ctx, tx, kind, reader.Epoch)
		if errors.Is(err, output.ErrNotFound) {
			// No row at all. That is a REAL state and not a failure: a plane
			// whose inventory controller has never started has no lease for
			// that kind, and a status surface that errored here would fail on
			// exactly the deployment whose missing controller is the thing
			// worth reporting.
			continue
		}
		if err != nil {
			return Status{}, err
		}
		if lease.OwnerID == "" {
			// No owner is not zero remaining: a kind nobody holds is a kind
			// nobody is working, and reporting it as "expired" would make an
			// idle plane look like a stuck one.
			continue
		}
		status.Leases[kind] = lease.Remaining(status.At)
	}

	status.Counts, err = reader.Repository.CountOutputPlaneState(ctx, tx, reader.Epoch)
	if err != nil {
		return Status{}, err
	}

	return status, nil
}

// DefaultStatusDebtLimit bounds one status read's debt scan.
const DefaultStatusDebtLimit = 500

func (reader *StatusReader) wired() error {
	if reader.Transactor == nil || reader.Repository == nil {
		return fmt.Errorf("%w: a status reader needs a transactor and a repository",
			output.ErrIncomplete)
	}
	if reader.Epoch <= 0 {
		return fmt.Errorf("%w: a status reader needs the activation epoch it speaks for",
			output.ErrIncomplete)
	}
	if reader.Bucket == "" {
		return fmt.Errorf("%w: a status reader needs the bucket fingerprint its cursor is "+
			"keyed by", output.ErrIncomplete)
	}

	return nil
}
