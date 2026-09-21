package hangaroutput

// What a coordinator needs from the world, declared where it is CONSUMED.
//
// PostgreSQL implements the repository in atc/db, where atc/db.Tx already
// exists; the node half is HTTP to a daemon; the announcer is whatever a
// deployment's diagnostics are. None of those belong to this package and none
// of them are named by it: an interface declared here is a statement of what
// this coordinator uses, and a package that satisfies one does not have to
// know it exists.
//
// Every name here is product-neutral. There is no build, job, check, Run,
// workflow, ticket, agent or playbook, and the one deployment concept that
// reaches this package -- how to find the node holding a source -- arrives as
// an opaque locator that is handed straight back to the dialer.

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// Transaction is the caller's transaction, plus the two things only its owner
// may do. The repository methods take output.Tx, which is deliberately
// ExecContext and QueryContext and nothing that could commit.
type Transaction interface {
	output.Tx

	Commit() error
	Rollback() error
}

// Transactor begins one.
//
// The coordinator owns its transactions because it is not composing with a
// consumer's binding write; a consumer that IS composing calls the repository
// with its own transaction and never touches this package.
type Transactor interface {
	Begin() (Transaction, error)
}

// Repository is the durable half.
type Repository interface {
	LoadHandoffRecord(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffRecord, error)

	// HangarDatabaseNow is the plane's clock, and the coordinator holds it for
	// exactly one decision: whether a capture whose object collided at a
	// server-derived key has passed its capture deadline. A coordinator whose
	// own clock had drifted forward would terminalize a capture that is still
	// entitled to register, and a terminal capture cannot be un-terminalized.
	HangarDatabaseNow(ctx context.Context, tx output.Tx) (output.Timestamp, error)

	CommitCaptureReservation(ctx context.Context, tx output.Tx, disposition output.SuccessfulFinishDisposition) (output.ReservationID, error)
	ResolveLogicalReservation(ctx context.Context, tx output.Tx, resolution output.LogicalResolution) error
	RecordFirstObjectCreate(ctx context.Context, tx output.Tx, reservation output.ReservationID, fence output.CaptureFence) error
	IssueStatChallenge(ctx context.Context, tx output.Tx, handoff output.HandoffID, reservation output.ReservationID, ref hangar.TreeRef, fence output.CaptureFence, keyID string, term time.Duration) (output.StatChallenge, error)
	RegisterReceipt(ctx context.Context, tx output.Tx, admission output.ReceiptAdmission) error
	RecordTerminalCaptureFailure(ctx context.Context, tx output.Tx, reservation output.ReservationID, fence output.CaptureFence, failure string) error

	// The Req 17 seal deadline, stamped once and evaluated in SQL. Both halves
	// are the repository's rather than the coordinator's because the deciding
	// clock is the database's: a capture crosses an ATC restart, so a deadline
	// a process holds is a deadline that dies with it.
	RecordSealDeadline(ctx context.Context, tx output.Tx, reservation output.ReservationID, fence output.CaptureFence, term time.Duration) (output.Timestamp, error)
	SealDeadlinePassed(ctx context.Context, tx output.Tx, reservation output.ReservationID) (bool, error)

	RecordNoCaptureIntent(ctx context.Context, tx output.Tx, disposition output.NoCaptureDisposition) error
	AcknowledgeNoCaptureRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement) error
	RecordPreReservationCancelIntent(ctx context.Context, tx output.Tx, disposition output.PreReservationCancelDisposition) error
	AcknowledgePreReservationCancelRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement) error
	AcknowledgeCaptureRelease(ctx context.Context, tx output.Tx, acknowledgement output.ReleaseAcknowledgement) error

	AcquireCaptureLease(ctx context.Context, tx output.Tx, reservation output.ReservationID, owner string, term time.Duration) (output.CaptureLease, error)

	ClassifyHandoff(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffStatus, error)
	CancelOrSettle(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffStatus, error)
}

// SourceControl is one node's authority over one source.
//
// It is the daemon's capture API, and the operations are exactly the ones a
// capture needs: what the seal did, what the bytes are, where they went, what
// the store says about them afterwards, and that the source may go.
type SourceControl interface {
	// Observe reports the exact execution's durable finish or stop
	// acknowledgement, or that none exists yet.
	//
	// The witness is the NODE's fact and it is read back through the
	// production route rather than kept anywhere here. Requirement 4 is exact
	// about what is not a witness -- pod state, a disappeared process, a
	// terminal row, an in-memory result -- and the way to honour that is to
	// have no second place the answer could come from.
	Observe(ctx context.Context, id executioncontrol.Identity,
		wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error)

	InspectSeal(ctx context.Context, handoff output.HandoffID, execution executioncontrol.Identity) (output.SealStarted, error)
	BeginSeal(ctx context.Context, request output.SealRequest) (output.SealStarted, error)
	ConfirmSeal(ctx context.Context, confirmation output.SealConfirmation) (output.CaptureAcknowledgement, error)

	// Canonicalize answers what the sealed tree is and creates nothing. It is a
	// separate call from Publish because requirement 21 puts a durable
	// resolution between them.
	Canonicalize(ctx context.Context, request output.PublicationRequest) (output.CanonicalizationResult, error)
	Publish(ctx context.Context, request output.PublicationRequest) (output.PublicationResult, error)

	// Attest answers a one-use challenge with a signed per-capture receipt. It
	// is a second call and not part of Publish because the challenge names the
	// generation the publish assigned, so it cannot exist a call earlier.
	Attest(ctx context.Context, challenge output.StatChallenge, claims output.ReceiptClaims) (output.Receipt, error)

	AcknowledgeRelease(ctx context.Context, intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error)
}

// SourceDialer reaches the one node holding a source.
//
// The locator is whatever was recorded when the incarnation was reserved, and
// this package neither builds nor parses one -- it hands back what it stored.
// A coordinator that could compose a locator would be a coordinator that could
// address a release to the wrong node.
type SourceDialer interface {
	ForLocator(locator string) (SourceControl, error)
}

// DrainConfirmer terminates every admitted writer for a sealed source and
// proves the boundary a seal confirmation needs.
//
// It is a port and not a method here because proving it is deployment work: for
// pod writers it means observing terminated status for every regular, init,
// sidecar and ephemeral container recorded for the exact old Pod UID before
// deleting that UID, and NotFound, Gone, force deletion or an unreachable
// kubelet is not proof. What this package owns is that the answer is required
// before a canonical read begins.
//
// Returning an error is how "I could not prove it" is said. That becomes a
// typed seal_unconfirmed, never a publication.
type DrainConfirmer interface {
	ConfirmDrain(ctx context.Context, locator string, started output.SealStarted) ([]output.DrainedWriter, error)
}

// ReceiptChecker verifies a receipt's signature against the activation-pinned
// key and the one-use challenge it answers.
//
// It is deliberately narrower than a full verification. Req 26 asks for an
// exact-generation metadata stat as well, and the control plane holds no bucket
// credential to make one with -- Req 20 puts the object role on the publisher
// principal and Req 24 keeps it away from everything else. The stat IS
// performed, by the daemon, inside the attestation the receipt comes from;
// what is left for the control plane is the half it can do and must:
// the signature under the epoch's key, and every signed claim matched against
// the durable checkpoint, reservation and fence, which this package does
// itself in checkReceiptClaims.
type ReceiptChecker interface {
	Verify(receipt output.Receipt, challenge output.StatChallenge) error
}

// Announcer puts what happened to a capture where the thing that owns the
// execution can show it. Requirement 18.
//
// It is deliberately not a logger: a log line is for an operator and this is
// for whoever is watching the execution. What a deployment does with it -- an
// event, a log line on the step -- is the deployment's, and the redaction rule
// is not: an announcement carries a disposition and a reason, and never a
// warrant, key, path or consumer reference.
//
// It takes the CALLER's transaction, and that is the whole of what makes an
// announcement survivable. An announcement emitted after the commit it
// announces is outside everything recovery re-takes: the fact is durable, the
// answer is lost, the transition is never taken again and the announcement is
// gone. That cost the terminal disposition -- the last one, the one nothing
// repeats and the one a build's diagnostics exist to show. Riding the
// transaction of the fact it announces makes the two one commit, and the store
// is idempotent by (handoff, kind) so a repeated transition tells a watcher
// nothing twice.
type Announcer interface {
	Announce(ctx context.Context, tx output.Tx, handoff output.HandoffID,
		announcement Announcement) error
}

// unavailableDialer is what a deployment with no output plane has.
//
// It refuses rather than being nil, so that a handoff which somehow exists
// without a runtime to reach is a bounded, typed refusal in a log rather than a
// nil dereference in a component that runs every minute.
type unavailableDialer struct{}

// NoSourcePlane returns a dialer that reaches nothing.
func NoSourcePlane() SourceDialer { return unavailableDialer{} }

func (unavailableDialer) ForLocator(locator string) (SourceControl, error) {
	return nil, fmt.Errorf("%w: this deployment has no output plane, and handoff sources on %q "+
		"cannot be reached", output.ErrInfrastructure, locator)
}

// unprovableDrain is the matching drain confirmer: it can prove nothing,
// which is `seal_unconfirmed` and never a publication.
type unprovableDrain struct{}

// NoDrainProof returns a drain confirmer that proves nothing.
func NoDrainProof() DrainConfirmer { return unprovableDrain{} }

func (unprovableDrain) ConfirmDrain(context.Context, string, output.SealStarted) ([]output.DrainedWriter, error) {
	return nil, fmt.Errorf("%w: this deployment cannot observe container termination, so no "+
		"writer drain can be proved", output.ErrSealUnconfirmed)
}
