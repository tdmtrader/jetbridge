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

	// OperationPolicyAttestation re-reads the bucket's lifetime policy and IAM
	// at least every MaxPolicyEvidenceAge. Its principal touches no object.
	OperationPolicyAttestation OperationKind = "policy_attestation"
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
		OperationPolicyAttestation,
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

// Deferred: the operator status and diagnosis surface is Phase 8's; no running
// process reads it yet
//
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
