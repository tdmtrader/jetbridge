package output

// What a coordinator has to KNOW before it may do anything.
//
// A capture crosses two systems, so recovery cannot ask "what was I doing" --
// there is no such fact anywhere. It can only ask "what is durably true", and
// then derive the one bounded transition that is legal next. HandoffRecord is
// that question's answer: every durable fact about one handoff, read in one
// place, with nothing derived and nothing remembered.
//
// It is NOT a wire type and carries no json tags on purpose. It is a read of
// PostgreSQL rows plus two observations of a node, assembled for a decision
// that happens in one process; freezing it would freeze a query.
//
// Two of its fields come from the node rather than the database, and they have
// to: the source ledger is authoritative for sealing (plan.md, "Sealing is
// daemon admission fencing plus Kubernetes proof"), so "has sealing begun" has
// no answer in PostgreSQL and a second copy there would be the copy that
// disagrees after a takeover.

import (
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// CaptureState is the reservation row's own lifecycle vocabulary. It is the
// schema's CHECK constraint, spelled once here so that a coordinator reading a
// state it does not know is a refusal rather than a silent default.
type CaptureState string

const (
	CaptureStateUnresolved CaptureState = "unresolved"
	CaptureStateResolved   CaptureState = "resolved"
	CaptureStateRegistered CaptureState = "registered"
	CaptureStateFailed     CaptureState = "failed"
	CaptureStateCancelled  CaptureState = "cancelled"
)

func CaptureStates() []CaptureState {
	return []CaptureState{
		CaptureStateUnresolved,
		CaptureStateResolved,
		CaptureStateRegistered,
		CaptureStateFailed,
		CaptureStateCancelled,
	}
}

func ParseCaptureState(value string) (CaptureState, error) {
	for _, member := range CaptureStates() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: capture state %q; the vocabulary is %v",
		ErrUnknownMember, value, CaptureStates())
}

// CaptureLease is one renewable, fenced warrant of capture ownership.
//
// It lives in the leaf rather than beside the SQL because the coordinator that
// renews it and the repository that issues it are in different packages, and a
// lease value only one of them can name would force the other to import the
// database.
type CaptureLease struct {
	ReservationID ReservationID
	OwnerID       string
	CaptureFence  CaptureFence
	ExpiresAt     Timestamp
}

// SourcePlacement is where a source incarnation physically is, in terms a
// product-neutral coordinator may hold.
//
// Locator is OPAQUE. It is whatever a deployment's source-control dialer needs
// to reach the one node that issued this incarnation, and Hangar neither parses
// it nor derives anything from it -- exactly as it neither parses nor derives
// an object key. A coordinator that could take it apart would be a coordinator
// that could point a release somewhere else.
type SourcePlacement struct {
	Locator     string
	Incarnation SourceIncarnation
	Directory   string
}

// Reserved reports whether a source incarnation exists on some node for this
// handoff.
//
// This is the fork every cancellation takes, and after the Phase 4 ruling it is
// no longer the same question as "was a hold acknowledged": the incarnation is
// reserved and its directory created BEFORE the producing Pod exists, so a
// cancellation that beats the control init still has bytes on a node to
// release. A branch that asked about the hold would leave that directory
// behind for every cancelled capture that never started.
func (placement SourcePlacement) Reserved() bool {
	return placement.Locator != "" && placement.Incarnation.Validate() == nil
}

// HandoffRecord is every durable fact about one handoff.
type HandoffRecord struct {
	// The predeclaration: what was true before anything could start.
	HandoffID       HandoffID
	SourceHoldID    SourceHoldID
	Execution       executioncontrol.Identity
	ActivationEpoch executioncontrol.ActivationEpoch
	Output          OutputName
	CaptureDeadline Timestamp

	// The two pre-start acknowledgements.
	Source           SourcePlacement
	HoldAcknowledged bool

	// The arbiter, and what it decided. Disposition is a pointer because an
	// unwon arbiter is the ABSENCE of a branch and not a fourth branch.
	Disposition                  *Disposition
	Settled                      bool
	PastIrreversiblePublishPoint bool

	// The capture branch's own row. Zero unless Disposition is capture.
	ReservationID        ReservationID
	ProducerCheckpointID OpaqueID
	CaptureFence         CaptureFence
	State                CaptureState
	Ref                  hangar.TreeRef
	TerminalFailure      string

	// The logical resolution, which is a distinct row and therefore a distinct
	// fact: a reservation may be committed with no resolution, and that is the
	// state the whole pre-create ordering exists to make observable.
	LogicalResolved bool
	Scope           hangar.Scope
	Digest          hangar.Digest

	// The release pair, on whichever branch owes one.
	ReleaseIntentID     ReleaseIntentID
	ReleaseAcknowledged bool

	// The receipt, present once one is registered.
	Receipt *Receipt

	// Cancellation is a REQUEST, not a state: a caller asked, and the arbiter
	// has not necessarily been won yet. It is separate from the disposition for
	// that reason -- "cancellation wins before Stage 2" is a race whose outcome
	// is the arbiter, and a coordinator that treated the request as the outcome
	// would be deciding it itself.
	CancellationRequested bool

	// The finish witness, when one exists. Nil is not "the producer failed": it
	// is "no authoritative outcome has been obtained", which is the state that
	// forbids every branch until reconciliation resolves it.
	FinishWitness *executioncontrol.Acknowledgement

	// FinishUnresolvable is a typed reconciliation: the exact outcome could not
	// be proved, or the ledger that would hold it is gone. It is not a failure
	// of the producer and it may never be rounded into one.
	FinishUnresolvable NoCaptureReason

	// The two node-side observations. The source ledger is authoritative for
	// sealing, so these have no PostgreSQL answer and a copy there would be the
	// copy that disagrees after a takeover.
	SealBegun     bool
	SealConfirmed bool
	SealDrainSet  []WriterTicketID
}

// Validate refuses a record that could not have come from the schema.
func (record HandoffRecord) Validate() error {
	if err := record.HandoffID.Validate(); err != nil {
		return err
	}
	if err := record.Execution.Validate(); err != nil {
		return err
	}
	if record.ActivationEpoch == 0 {
		return fmt.Errorf("%w: the handoff record names no activation epoch", ErrIncomplete)
	}
	if record.HoldAcknowledged && !record.Source.Reserved() {
		return fmt.Errorf("%w: handoff %s has an acknowledged hold over no reserved source; a "+
			"hold binds to an incarnation this node issued first", ErrInvalidIdentity,
			record.HandoffID)
	}
	if record.Disposition != nil {
		if err := record.Disposition.Validate(); err != nil {
			return err
		}
	}
	if record.State != "" {
		if _, err := ParseCaptureState(string(record.State)); err != nil {
			return err
		}
	}

	return nil
}

// HasCaptureReservation reports whether Stage 2 committed.
//
// It reads the reservation id and not the arbiter, deliberately. Winning the
// arbiter is not capture authority; the reservation row is the thing later
// stages are served from, and a coordinator that took the arbiter for the
// reservation would seal from a decision rather than from a commit.
func (record HandoffRecord) HasCaptureReservation() bool {
	return record.ReservationID.Validate() == nil
}
