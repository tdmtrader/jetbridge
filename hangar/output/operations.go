package output

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// OperationKind is the closed set of durable work this plane performs.
//
// It is a vocabulary rather than a set of booleans on one worker because every
// one of these owns a SEPARATE durable lease, cursor, debt and fencing epoch.
// One operation cannot advance another's cursor, and a bad object in one cannot
// starve another: that guarantee is only expressible if the kinds are distinct
// values, and it is why the kind is half the lease's primary key rather than a
// column on a shared row.
//
// The members are the nine the schema's CHECK names. They are product-neutral:
// there is no Run, build, job, check, workflow, ticket or agent here, and
// nothing in the set says why a capture was made.
type OperationKind string

const (
	// OperationCaptureRecovery advances every incomplete capture by one
	// transition. It runs in the web node, because it needs PostgreSQL,
	// Kubernetes and the output daemon, and no output-bucket role at all.
	OperationCaptureRecovery OperationKind = "capture_recovery"

	// OperationNoCaptureRelease settles the branch that publishes nothing and
	// still owes the producer's source back.
	OperationNoCaptureRelease OperationKind = "no_capture_release"

	// OperationInventory sweeps the dedicated output bucket. It is the only
	// kind whose principal holds bucket-wide list.
	OperationInventory OperationKind = "inventory"

	// OperationAdoption brings a marked, uncorrelated, out-of-grace generation
	// into lifecycle bookkeeping. It is separate from inventory because a
	// sweep that could not classify an object must still not be prevented from
	// adopting the ones it could.
	OperationAdoption OperationKind = "adoption"

	// The three halves of reclamation. They are three kinds and not one
	// because the middle one is the only one that talks to the object store,
	// and a lease that covered all three would have to be long enough for the
	// slowest of them.
	OperationReclaimAdmission    OperationKind = "reclaim_admission"
	OperationReclaimDelete       OperationKind = "reclaim_delete"
	OperationReclaimFinalization OperationKind = "reclaim_finalization"

	// OperationReadLeaseCleanup closes read leases whose owners are gone. It
	// is what stops one crashed materializer from pinning a generation against
	// reclaim for the life of the deployment, and it closes a lease only after
	// the DATABASE clock says it expired.
	OperationReadLeaseCleanup OperationKind = "read_lease_cleanup"
)

// OperationKinds is the closed set, in the order the schema names them.
// NotifyChannel is the PostgreSQL notification channel one kind is woken on.
//
// One channel per kind, matching one lease and one cursor per kind, so a
// notification about reclaim work cannot wake the inventory sweep -- which
// would be a wake with nothing to do, every time, for the kind with the most
// expensive pass.
//
// It is derived rather than written down because a channel name that drifted
// from its kind would be a producer notifying nobody, and the failure mode of
// that is silence: the work is still found by the periodic pass, later, and
// nothing says the acceleration stopped working.
//
// WHICH KINDS ACCELERATE, AND WHY THE OTHER EIGHT DO NOT.
//
// Every kind has an unconditional periodic wake and none of them depends on a
// notification to find work; Req 50 asks for the fallback "in addition to
// notifications", and the fallback is what is load-bearing. So the question a
// kind answers here is only whether a wake sooner than the tick is worth a
// producer, and one-of-nine with no stated reason reads as unfinished rather
// than as chosen. TestOnlyTheRecordedOperationKindsAreAccelerated is what keeps
// these sentences true.
//
//	Acceleration: reclaim_delete -- ACCELERATED. AdmitReclaim mints a job inside
//	a transaction, and until that job is taken the object is decided-on and
//	still present. This is the one kind where the latency between "the plane
//	decided to delete" and "the object is gone" is a window an operator can
//	observe in the bucket.
//
//	Acceleration: inventory -- none. There is no database write to notify from:
//	the sweep is driven by what is in the BUCKET, and nothing in PostgreSQL
//	knows an object appeared. A producer would have to be the bucket.
//
//	Acceleration: adoption -- none. Same reason, plus a clock: an object becomes
//	adoptable when publication grace elapses, which is the passage of time and
//	not an event. Nothing writes "grace has now elapsed".
//
//	Acceleration: reclaim_admission -- none. It reads candidates whose grace has
//	elapsed, so its trigger is also the clock. Waking it at the instant a
//	generation settles would find a candidate that is days from admissible.
//
//	Acceleration: read_lease_cleanup -- none. A lease becomes abandoned by
//	expiring, on the database clock, and expiry is not a write.
//
// The three below DO have a database-write trigger and are still not
// accelerated. Their reason used to name Phase 5, which was already closed when
// it was written -- a deferral addressed to a phase whose boxes do not contain
// it is not a deferral. The live owner is the operator status surface
// (atc/hangaroutput.StatusReader and the alert rules beside it), and the reason
// is that a wake-up latency and a stalled sweep are the same operator concern:
// what matters about an unaccelerated kind is not that it waits up to a minute,
// it is whether anybody would notice if it waited forever. The status surface
// publishes each kind's lease term as a series, so a kind nobody is working is
// an alert rather than an inference, and adding a producer beside a transition
// is then a measured improvement rather than a hedge against an invisible
// failure.
//
//	Acceleration: capture_recovery -- none. An incomplete handoff becomes
//	recoverable when an outcome or a deadline is written, so a producer is
//	possible. The periodic fallback bounds it at a minute, and
//	concourse_hangar_output_operation_lease_remaining_seconds{kind="capture_recovery"}
//	is what says whether the worker is running at all.
//
//	Acceleration: no_capture_release -- none, for the same reason: recording a
//	no-capture intent is a write, and the release that follows it waits for the
//	tick. Nothing is at stake in the bucket while it waits; the source hold is
//	the thing held, and it is held on one node.
//
//	Acceleration: reclaim_finalization -- none, and it is the least urgent of
//	the three. FinalizeReclaim follows an acknowledged delete, so the object is
//	already gone by the time this runs: the latency costs bookkeeping rather
//	than an observable state, and the backlog it would show up in is
//	concourse_hangar_output_plane_inventory{kind="unfinalized_reclaim_jobs"},
//	which has an alert of its own.
func NotifyChannel(kind OperationKind) string {
	return "hangar_output_" + string(kind)
}

func OperationKinds() []OperationKind {
	return []OperationKind{
		OperationCaptureRecovery,
		OperationNoCaptureRelease,
		OperationInventory,
		OperationAdoption,
		OperationReclaimAdmission,
		OperationReclaimDelete,
		OperationReclaimFinalization,
		OperationReadLeaseCleanup,
	}
}

func ParseOperationKind(value string) (OperationKind, error) {
	for _, member := range OperationKinds() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: operation kind %q; the vocabulary is %v",
		ErrUnknownMember, value, OperationKinds())
}

func (kind *OperationKind) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseOperationKind(text)
	if err != nil {
		return err
	}
	*kind = parsed

	return nil
}

func (kind OperationKind) Validate() error {
	_, err := ParseOperationKind(string(kind))

	return err
}

// OperationLease is one kind's durable ownership of its own work.
//
// Fence is monotonic and advances on takeover. An expired owner may not delete
// or finalize anything, and the fence is how a write says which owner it is
// from: a lease that had only an expiry would let a paused owner wake up after
// a takeover and finish work under authority somebody else now holds.
// It carries no json tags, deliberately. A lease never crosses a wire: it is a
// row one controller reads and writes, and a type that said it were frozen
// would be promising a shape to an implementation that does not exist.
type OperationLease struct {
	Kind            OperationKind
	ActivationEpoch executioncontrol.ActivationEpoch
	OwnerID         string
	LeaseFence      LeaseFence
	RenewedAt       Timestamp
	ExpiresAt       Timestamp
}

func (lease OperationLease) Validate() error {
	if err := lease.Kind.Validate(); err != nil {
		return err
	}
	if lease.ActivationEpoch == 0 {
		return fmt.Errorf("%w: operation lease names no activation epoch", ErrIncomplete)
	}
	// The owner is a UUID because the column is, and because a controller's
	// identity is generated per process rather than configured: a deployment
	// that named its owners would have two processes sharing one name the first
	// time it scaled, and the lease would stop being a lease.
	if err := validateUUID("operation lease owner", lease.OwnerID); err != nil {
		return err
	}
	if lease.LeaseFence == 0 {
		return fmt.Errorf("%w: operation lease fence is zero; an expired owner cannot delete or "+
			"finalize, and the fence is how a write says which owner it is from", ErrIncomplete)
	}

	return nil
}

// Remaining is how much of the lease is left at the given database-clock
// instant. It is never computed from a node's own clock: `now` is a reading
// this plane took from PostgreSQL.
func (lease OperationLease) Remaining(now Timestamp) time.Duration {
	return lease.ExpiresAt.UTC().Sub(now.UTC())
}

// ReclaimOutcome is how a reclaim job ended.
//
// Four members, and the difference between the first two is the whole point of
// Req 49. `reclaimed_confirmed` needs an acknowledged conditional delete;
// `reclaimed_inferred` is a durable admitted-delete record whose response was
// lost, plus observed exact absence. They are not the same evidence and this
// plane never lets one stand in for the other, because confirming a deletion it
// did not see acknowledged is how a lifetime violation becomes a normal
// reclamation in the record.
type ReclaimOutcome string

const (
	// ReclaimConfirmed is an acknowledged conditional delete.
	ReclaimConfirmed ReclaimOutcome = "reclaimed_confirmed"

	// ReclaimInferred is a durable admitted delete, a lost response, and exact
	// absence observed afterwards. Absence WITHOUT a prior admitted delete is
	// an out-of-band lifetime violation and never this.
	ReclaimInferred ReclaimOutcome = "reclaimed_inferred"

	// ReclaimConflicted is a replacement generation, metageneration or marker
	// found where the exact one was expected. It becomes debt and never
	// broadens into an unconditional delete.
	ReclaimConflicted ReclaimOutcome = "conflicted"

	// ReclaimAbandoned is a job given up on -- an unauthorized principal, a
	// policy that went at-risk before any delete was admitted. The generation
	// goes back to being protected rather than being deleted on a guess.
	ReclaimAbandoned ReclaimOutcome = "abandoned"
)

func ReclaimOutcomes() []ReclaimOutcome {
	return []ReclaimOutcome{
		ReclaimConfirmed,
		ReclaimInferred,
		ReclaimConflicted,
		ReclaimAbandoned,
	}
}

func ParseReclaimOutcome(value string) (ReclaimOutcome, error) {
	for _, member := range ReclaimOutcomes() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: reclaim outcome %q; the vocabulary is %v",
		ErrUnknownMember, value, ReclaimOutcomes())
}

func (outcome *ReclaimOutcome) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseReclaimOutcome(text)
	if err != nil {
		return err
	}
	*outcome = parsed

	return nil
}

func (outcome ReclaimOutcome) Validate() error {
	_, err := ParseReclaimOutcome(string(outcome))

	return err
}

// PlaneCounts is the output plane's inventory, as an operator reads it.
//
// Counts and not lists. An operator watching a plane wants to know whether the
// numbers are moving, and a status surface that returned every live generation
// would be the thing that fell over on the deployment where the number mattered.
type PlaneCounts struct {
	// LiveGenerations are registered, adopted or reclaiming objects: the set
	// this plane is responsible for, and the set a downgrade cannot abandon.
	LiveGenerations int

	// NonterminalCaptures may still create an object or still owe a
	// registration. They are what a capture deadline eventually settles.
	NonterminalCaptures int

	// OpenClaims and OpenReadLeases are the protections consumers hold. Each
	// one keeps reclaim admission refusing for the generation it names, so an
	// open lease nobody is using is an object nothing will ever delete.
	OpenClaims     int
	OpenReadLeases int

	// UnfinalizedReclaimJobs were admitted and have no outcome. Every one of
	// them is an object whose disposition is unknown: the delete may have
	// happened, and the answer may have been lost.
	UnfinalizedReclaimJobs int
}
