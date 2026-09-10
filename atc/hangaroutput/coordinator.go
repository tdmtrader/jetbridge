package hangaroutput

// The impure half: performing the one transition Decide selected.
//
// Two rules run through every method here and they are the reason the file is
// shaped as it is.
//
// NO DATABASE LOCK IS HELD ACROSS A NETWORK CALL. Every transition that talks
// to a node or a store does its reads in one short transaction, closes it,
// makes the call, and commits the result in a second short transaction. That
// is not a performance choice: a transaction held open across a daemon call is
// a lock held for as long as an unreachable node takes to time out, and the
// lock order this plane depends on stops meaning anything.
//
// NOTHING HERE IS DESCRIBED AS ATOMIC. Every cross-system step is two steps and
// idempotent, and recovery repeats the same identity until committed-versus-not
// is known. An ambiguous answer is never resolved by guessing; it is resolved by
// asking again with the same identity, which is why every request this file
// composes is derived from durable state rather than from a value it kept.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/output"
)

// Default terms. All three are database-clock terms; none of them is read from
// a process's own clock.
const (
	// DefaultLeaseTerm is requirement 10's 15-minute capture ownership lease.
	DefaultLeaseTerm = 15 * time.Minute

	// DefaultSealDeadline is requirement 17's 5 minutes, configurable from 30
	// seconds through 30 minutes.
	DefaultSealDeadline = 5 * time.Minute

	// DefaultChallengeTerm bounds a stat challenge. A receipt signed over old
	// facts proves only that the facts were once true.
	DefaultChallengeTerm = 5 * time.Minute
)

// Coordinator advances one handoff by one bounded transition.
//
// It holds no state about any handoff between calls. That is the whole point:
// a coordinator with a memory would be a coordinator whose memory disagrees
// with PostgreSQL after a restart, and the restart is the case this exists for.
type Coordinator struct {
	Transactor Transactor
	Repository Repository
	Dialer     SourceDialer
	Drain      DrainConfirmer
	Verifier   ReceiptChecker
	Announcer  Announcer

	// OwnerID identifies this process for the ownership lease. Two processes
	// sharing one would be two owners the fence cannot tell apart.
	OwnerID string

	// ReceiptKeyID names the key a receipt for this epoch must be signed under.
	ReceiptKeyID string

	LeaseTerm     time.Duration
	SealDeadline  time.Duration
	ChallengeTerm time.Duration

	// Now is the database's clock only for values that are compared to nothing
	// -- a signature timestamp, a deadline offered to a daemon. Every deadline
	// that DECIDES anything is evaluated in SQL as `now()`.
	Now func() time.Time
}

func (coordinator *Coordinator) leaseTerm() time.Duration {
	if coordinator.LeaseTerm == 0 {
		return DefaultLeaseTerm
	}

	return coordinator.LeaseTerm
}

func (coordinator *Coordinator) sealDeadline() time.Duration {
	if coordinator.SealDeadline == 0 {
		return DefaultSealDeadline
	}

	return coordinator.SealDeadline
}

func (coordinator *Coordinator) challengeTerm() time.Duration {
	if coordinator.ChallengeTerm == 0 {
		return DefaultChallengeTerm
	}

	return coordinator.ChallengeTerm
}

func (coordinator *Coordinator) now() time.Time {
	if coordinator.Now == nil {
		return time.Now().UTC()
	}

	return coordinator.Now().UTC()
}

// Advance performs at most one bounded transition and reports which.
//
// One, and not a loop, because a loop would hold a worker on one handoff while
// a node it cannot reach times out repeatedly. The caller runs Advance again --
// on a notification, or on the periodic fallback -- and the next call reads the
// state the last one committed.
func (coordinator *Coordinator) Advance(ctx context.Context, handoff output.HandoffID) (Decision, error) {
	record, err := coordinator.observe(ctx, handoff)
	if err != nil {
		return Decision{}, err
	}

	return coordinator.perform(ctx, record)
}

// Cancel is the product-neutral cancel/settle seam's caller-facing half.
//
// It is a separate entry point and not a flag on a row, because a cancellation
// REQUEST is not durable state: what is durable is which branch the arbiter
// then won. Before Stage 2 that is pre_reservation_cancel and only ever that;
// after it, the capture is immutable and this drives receipt-or-orphan
// settlement. Neither can create a consumer binding, and there is no parameter
// here through which one could be asked for.
//
// Classification precedes the destructive half by construction: the record is
// read, the transition is derived from it, and only then does anything happen.
func (coordinator *Coordinator) Cancel(ctx context.Context, handoff output.HandoffID) (Decision, error) {
	record, err := coordinator.observe(ctx, handoff)
	if err != nil {
		return Decision{}, err
	}
	record.CancellationRequested = true

	return coordinator.perform(ctx, record)
}

func (coordinator *Coordinator) perform(ctx context.Context, record output.HandoffRecord) (Decision, error) {
	decision, err := Decide(record)
	if err != nil {
		return Decision{}, err
	}

	switch decision.Transition {
	case TransitionAwaitOutcome:
		return decision, nil

	case TransitionNone:
		// Nothing, and deliberately nothing.
		//
		// A replay of the terminal announcement lived here, and it was
		// unreachable from the component that exists to recover a capture:
		// `Recoverer.Run` advances every INCOMPLETE handoff, and a handoff that
		// would decide `none` has settled and is not listed. Every terminal
		// announcement is now written inside the transaction that commits its
		// disposition, which is where it is owed and the one place a lost
		// answer cannot separate it from the fact.
		return decision, nil

	case TransitionCommitCaptureReservation:
		return decision, coordinator.commitStageTwo(ctx, record)
	case TransitionRecordNoCaptureIntent:
		return decision, coordinator.recordNoCapture(ctx, record)
	case TransitionAcknowledgeNoCaptureRelease:
		return decision, coordinator.releaseFor(ctx, record, output.DispositionNoCapture)
	case TransitionRecordCancellationIntent:
		return decision, coordinator.recordCancellation(ctx, record)
	case TransitionAcknowledgeCancellationRelease:
		return decision, coordinator.releaseFor(ctx, record, output.DispositionPreReservationCancel)

	case TransitionCancelCapture:
		return decision, coordinator.cancelCapture(ctx, record)
	case TransitionBeginSeal:
		return decision, coordinator.beginSeal(ctx, record)
	case TransitionConfirmSeal:
		return decision, coordinator.confirmSeal(ctx, record)
	case TransitionResolveLogicalReservation:
		return decision, coordinator.resolveLogical(ctx, record)
	case TransitionPublish:
		return decision, coordinator.publish(ctx, record)
	case TransitionRegisterReceipt:
		return decision, coordinator.registerReceipt(ctx, record)
	case TransitionReleaseSource:
		return decision, coordinator.releaseFor(ctx, record, output.DispositionCapture)
	case TransitionSettleOrphan:
		return decision, coordinator.settleOrphan(ctx, record)
	}

	return decision, fmt.Errorf("%w: transition %q has no implementation",
		output.ErrUnknownMember, decision.Transition)
}

// observe reads the durable record and, for a live capture, asks the node what
// its seal has done.
//
// The node question is asked OUTSIDE the transaction that read the row, and the
// order is deliberate: the row is the authority for what branch this is, and
// the node is the authority for the seal. Reading them in one transaction would
// be a database lock held across a network call.
func (coordinator *Coordinator) observe(ctx context.Context, handoff output.HandoffID) (output.HandoffRecord, error) {
	record, err := coordinator.read(ctx, func(tx Transaction) (output.HandoffRecord, error) {
		return coordinator.Repository.LoadHandoffRecord(ctx, tx, handoff)
	})
	if err != nil {
		return output.HandoffRecord{}, err
	}

	if !record.Source.Reserved() {
		return record, nil
	}

	control, err := coordinator.Dialer.ForLocator(record.Source.Locator)
	if err != nil {
		return output.HandoffRecord{}, err
	}

	// Before the arbiter is won, the question is the finish witness, and it is
	// the node's. There is deliberately no second place it could come from:
	// requirement 4 says pod state, a disappeared process, a terminal row and
	// an in-memory result are not witnesses, and the way to honour that is to
	// have nowhere else to read one.
	if record.Disposition == nil {
		observed, err := control.Observe(ctx, record.Execution, 0)
		if err != nil {
			return output.HandoffRecord{}, err
		}
		if observed.Acknowledgement != nil && observed.Acknowledgement.Kind != "" {
			witness := *observed.Acknowledgement
			record.FinishWitness = &witness
		}

		return record, nil
	}

	if *record.Disposition != output.DispositionCapture ||
		record.State != output.CaptureStateUnresolved {
		return record, nil
	}

	started, err := control.InspectSeal(ctx, record.HandoffID, record.Execution)
	if errors.Is(err, output.ErrNotFound) {
		// No seal has begun. That is an answer, not a failure: an empty drain
		// set is a real and different answer -- nobody was writing when
		// admission was fenced -- and treating the two the same would let a
		// coordinator confirm a seal that never started.
		return record, nil
	}
	if err != nil {
		return output.HandoffRecord{}, err
	}

	record.SealBegun = true
	record.SealDrainSet = started.DrainSet
	record.SealConfirmed = started.Confirmed

	return record, nil
}

// read runs a read-only transaction and always rolls it back.
func (coordinator *Coordinator) read(ctx context.Context, body func(Transaction) (output.HandoffRecord, error)) (output.HandoffRecord, error) {
	tx, err := coordinator.Transactor.Begin()
	if err != nil {
		return output.HandoffRecord{}, err
	}
	defer tx.Rollback()

	return body(tx)
}

// write runs a short transaction and commits it.
//
// An error from Commit is AMBIGUOUS and is returned as such: the transaction
// may have committed. Nothing above it may treat a commit error as "it did not
// happen"; the next Advance reads what is durably there and decides again,
// which is the only honest way to resolve it.
func (coordinator *Coordinator) write(ctx context.Context, body func(Transaction) error) error {
	tx, err := coordinator.Transactor.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := body(tx); err != nil {
		return err
	}

	return tx.Commit()
}

// own takes or renews the capture ownership lease and returns the fence every
// later operation in this transition is admitted under.
//
// It is called at the start of every capture transition rather than once,
// because a takeover between two transitions is exactly what the fence exists
// to catch: the repository refuses a stale fence, so a coordinator that kept a
// fence from an earlier call would be refused rather than allowed -- which is
// the right direction, and this makes it observable at the top of the step
// instead of three calls later.
func (coordinator *Coordinator) own(ctx context.Context, record output.HandoffRecord) (output.CaptureLease, error) {
	var lease output.CaptureLease
	err := coordinator.write(ctx, func(tx Transaction) error {
		var err error
		lease, err = coordinator.Repository.AcquireCaptureLease(ctx, tx,
			record.ReservationID, coordinator.OwnerID, coordinator.leaseTerm())

		return err
	})

	return lease, err
}

func (coordinator *Coordinator) control(record output.HandoffRecord) (SourceControl, error) {
	if !record.Source.Reserved() {
		return nil, fmt.Errorf("%w: handoff %s has no source on any node to reach",
			output.ErrNotFound, record.HandoffID)
	}

	return coordinator.Dialer.ForLocator(record.Source.Locator)
}

// commitStageTwo is the successful-finish-only door.
//
// The producer checkpoint id is derived from the handoff rather than minted, so
// that a repeat after an ambiguous commit offers the SAME checkpoint and is
// idempotent. A fresh uuid here would make every retry look like a different
// Stage 2 wearing an old idempotency key, which the repository correctly
// refuses -- and the capture would be stuck forever on a lost commit response.
func (coordinator *Coordinator) commitStageTwo(ctx context.Context, record output.HandoffRecord) error {
	if record.FinishWitness == nil {
		return fmt.Errorf("%w: Stage 2 for handoff %s has no finish witness",
			output.ErrIncomplete, record.HandoffID)
	}

	// The SELECTION announcement rides Stage 2's own commit.
	//
	// Selection IS this commit: post-completion hijack becomes unavailable the
	// moment the capture is durably chosen, and not a moment earlier. Said
	// before it, an announcement outlives a Stage 2 that never committed and
	// tells a watcher hijack is gone when it is not; said after it, a lost
	// answer leaves a committed capture nobody was told about. One transaction
	// is the only spelling with neither failure, and it is the same rule the
	// terminal announcement follows.
	return coordinator.write(ctx, func(tx Transaction) error {
		if _, err := coordinator.Repository.CommitCaptureReservation(ctx, tx,
			output.SuccessfulFinishDisposition{
				ProtocolVersion:       output.ProtocolVersion,
				Disposition:           output.DispositionCapture,
				Execution:             record.Execution,
				ActivationEpoch:       record.ActivationEpoch,
				HandoffID:             record.HandoffID,
				SourceLeaseID:         record.SourceLeaseID,
				ProducerCheckpointID:  checkpointFor(record.HandoffID),
				Output:                record.Output,
				CaptureFence:          1,
				CaptureDeadline:       record.CaptureDeadline,
				FinishAcknowledgement: *record.FinishWitness,
			}); err != nil {
			return err
		}

		return coordinator.say(ctx, tx, record, coordinator.announce(
			AnnouncementSelected, "", "post_completion_hijack_unavailable"))
	})
}

// recordNoCapture is the first of the branch's two halves.
func (coordinator *Coordinator) recordNoCapture(ctx context.Context, record output.HandoffRecord) error {
	disposition := output.NoCaptureDisposition{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     output.DispositionNoCapture,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		HandoffID:       record.HandoffID,
		SourceLeaseID:   record.SourceLeaseID,
		ReleaseIntentID: releaseIntentFor(record.HandoffID, output.DispositionNoCapture),
	}

	switch {
	case record.FinishWitness != nil:
		disposition.Reason = output.NoCaptureAuthoritativeNonSuccess
		disposition.FinishAcknowledgement = record.FinishWitness
	case record.FinishUnresolvable != "":
		// A reconciliation carries no witness, because it exists precisely
		// because none could be obtained.
		disposition.Reason = record.FinishUnresolvable
	default:
		return fmt.Errorf("%w: handoff %s reached no_capture with neither a witness nor a typed "+
			"reconciliation", output.ErrIncomplete, record.HandoffID)
	}

	return coordinator.write(ctx, func(tx Transaction) error {
		if err := coordinator.Repository.RecordNoCaptureIntent(ctx, tx, disposition); err != nil {
			return err
		}

		return coordinator.say(ctx, tx, record, coordinator.announce(AnnouncementDisposition,
			output.DispositionNoCapture, string(disposition.Reason)))
	})
}

// recordCancellation wins the third branch through the generic seam.
//
// It goes through CancelOrSettle rather than composing a disposition, because
// the seam is what a product-neutral caller uses and a second path into the
// same branch is a second set of rules for it. What CancelOrSettle does with a
// reserved source -- record a fenced release intent rather than close -- is the
// branch's own, and this does not re-decide it.
func (coordinator *Coordinator) recordCancellation(ctx context.Context, record output.HandoffRecord) error {
	return coordinator.write(ctx, func(tx Transaction) error {
		if _, err := coordinator.Repository.CancelOrSettle(ctx, tx, record.HandoffID); err != nil {
			return err
		}

		return coordinator.say(ctx, tx, record, coordinator.announce(AnnouncementDisposition,
			output.DispositionPreReservationCancel, "cancelled"))
	})
}

// cancelCapture is cancellation after Stage 2 and before the publish point.
func (coordinator *Coordinator) cancelCapture(ctx context.Context, record output.HandoffRecord) error {
	return coordinator.write(ctx, func(tx Transaction) error {
		if _, err := coordinator.Repository.CancelOrSettle(ctx, tx, record.HandoffID); err != nil {
			return err
		}

		return coordinator.say(ctx, tx, record, coordinator.announce(AnnouncementDisposition,
			output.DispositionCapture, "cancelled"))
	})
}

// releaseFor is the second half of every branch that owes a fenced release.
//
// One method for all three, because it is one protocol: the intent is durable,
// the daemon acknowledges that exact intent, and the caller records the
// acknowledgement. The branch decides only which recording method takes it.
func (coordinator *Coordinator) releaseFor(ctx context.Context, record output.HandoffRecord, branch output.Disposition) error {
	// The capture branch takes the lease first, exactly as every other capture
	// transition does. It was the one that did not, so a superseded coordinator
	// could still drive the daemon's AcknowledgeRelease for a capture it no
	// longer owned. Bounded -- the release is idempotent by intent and the row
	// is already terminal -- and Req 10 nonetheless names release in the list a
	// stale owner may not do, so the refusal belongs at the top of the step
	// where every other one is.
	//
	// The other two branches have no capture fence to take: they never reached
	// Stage 2, so there is no reservation to own, and their fence is the
	// EXECUTION fence the daemon checks.
	if branch == output.DispositionCapture {
		if _, err := coordinator.own(ctx, record); err != nil {
			return err
		}
	}

	control, err := coordinator.control(record)
	if err != nil {
		return err
	}

	intent := record.ReleaseIntentID
	if intent == "" {
		intent = releaseIntentFor(record.HandoffID, branch)
	}

	// Outside every database lock. A node that cannot answer holds up this one
	// handoff and nothing else.
	acknowledgement, err := control.AcknowledgeRelease(ctx, output.ReleaseIntent{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     branch,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		HandoffID:       record.HandoffID,
		SourceLeaseID:   record.SourceLeaseID,
		ReleaseIntentID: intent,
		Incarnation:     record.Source.Incarnation,
	})
	if err != nil {
		return err
	}

	return coordinator.write(ctx, func(tx Transaction) error {
		switch branch {
		case output.DispositionNoCapture:
			return coordinator.Repository.AcknowledgeNoCaptureRelease(ctx, tx, acknowledgement)
		case output.DispositionPreReservationCancel:
			return coordinator.Repository.AcknowledgePreReservationCancelRelease(ctx, tx, acknowledgement)
		case output.DispositionCapture:
			return coordinator.Repository.AcknowledgeCaptureRelease(ctx, tx, acknowledgement)
		}

		return fmt.Errorf("%w: branch %q owes no release", output.ErrUnknownMember, branch)
	})
}

// beginSeal fences writer admission and captures the drain set.
func (coordinator *Coordinator) beginSeal(ctx context.Context, record output.HandoffRecord) error {
	if err := requireCaptureAuthority(record, "begin_seal"); err != nil {
		return err
	}
	lease, err := coordinator.own(ctx, record)
	if err != nil {
		return err
	}
	control, err := coordinator.control(record)
	if err != nil {
		return err
	}

	// The deadline is committed BEFORE the seal begins, and it is the
	// database's own `now()` rather than this process's. A deadline composed
	// after the fact would bound nothing after a crash between the two, and a
	// deadline this process merely held would expire when the process did --
	// which is exactly the window a capture crossing an ATC restart lives in.
	var deadline output.Timestamp
	if err := coordinator.write(ctx, func(tx Transaction) error {
		var err error
		deadline, err = coordinator.Repository.RecordSealDeadline(ctx, tx,
			record.ReservationID, lease.CaptureFence, coordinator.sealDeadline())
		if err != nil {
			return err
		}

		// The boundary announcement rides the deadline's commit: the moment
		// this capture is durably sealing is the moment a watcher is owed the
		// news that its writers, sidecars and hijack session are going away.
		return coordinator.say(ctx, tx, record, coordinator.announce(
			AnnouncementSealStarted, output.DispositionCapture, "sealing"))
	}); err != nil {
		return err
	}

	_, err = control.BeginSeal(ctx, output.SealRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		HandoffID:       record.HandoffID,
		Incarnation:     record.Source.Incarnation,
		CaptureFence:    lease.CaptureFence,
		DeadlineAt:      deadline,
	})

	return err
}

// confirmSeal drains the captured set and proves the container boundary.
//
// A drain that cannot be PROVED is `seal_unconfirmed` and is recorded as a
// terminal capture failure: requirement 17 says an unconfirmed ticket drain or
// container boundary publishes no receipt and follows the no-re-execution rule.
//
// What counts as "cannot be proved" is the whole of this method's care, and it
// is a TYPE and not "an error happened". A lost HTTP response, an apiserver
// that did not answer this second and a node that is briefly unreachable are
// statements about the network; ErrSealUnconfirmed is the only statement about
// the boundary. Committing the first three as the fourth destroys a healthy,
// fully produced output on one dropped packet -- and the daemon's own record
// still says the seal is confirmed, so the guess is not even the likelier
// answer. Everything but the typed refusal is returned, and the next pass asks
// again with the same identity (Req 5).
func (coordinator *Coordinator) confirmSeal(ctx context.Context, record output.HandoffRecord) error {
	if err := requireCaptureAuthority(record, "confirm_seal"); err != nil {
		return err
	}
	lease, err := coordinator.own(ctx, record)
	if err != nil {
		return err
	}
	control, err := coordinator.control(record)
	if err != nil {
		return err
	}

	started, err := control.InspectSeal(ctx, record.HandoffID, record.Execution)
	if err != nil {
		return err
	}

	// ADMISSIBILITY, before anything is asked of the deployment or the node.
	//
	// Requirement 17's deadline is a database-clock deadline, and a deadline
	// that only decides whether an already-refused boundary is PERMANENT
	// leaves the question it exists to answer -- may a proof that arrives late
	// be admitted? -- to whichever wall clock is asked. That was the daemon's,
	// which is the clock Req 17 explicitly does not name and the one that
	// drifts: a node an hour behind admitted a seal the database had already
	// closed, and a node an hour ahead refused every seal proved in its last
	// hour. The daemon's own comparison stays, as the defence in depth its
	// comment says it is.
	//
	// The node is asked FIRST, and only then the clock: a confirmation this
	// node has already recorded is a fact, and refusing to read back a
	// statement it made -- because the answer was lost, or because this pass is
	// a takeover -- would turn a lost answer into a destroyed output. Only a
	// seal with nothing recorded is closed by the deadline.
	//
	// And the drain is not driven for a capture that is already inadmissible.
	// Proving the boundary TERMINATES the producing Pod, its sidecars and any
	// live hijack session; doing that to reach a refusal that is already
	// decided is destroying a step's containers to learn nothing.
	if !started.Confirmed {
		passed, err := coordinator.sealDeadlinePassed(ctx, record)
		if err != nil {
			return err
		}
		if passed {
			return coordinator.failTerminally(ctx, record, lease.CaptureFence, "seal_unconfirmed")
		}
	}

	drained, err := coordinator.Drain.ConfirmDrain(ctx, record.Source.Locator, started)
	if err != nil {
		return coordinator.sealUnprovable(ctx, record, lease.CaptureFence, err)
	}

	if _, err := control.ConfirmSeal(ctx, output.SealConfirmation{
		Started:      started,
		Drained:      drained,
		CaptureFence: lease.CaptureFence,
		ObservedAt:   output.NewTimestamp(coordinator.now()),
	}); err != nil {
		return coordinator.sealUnprovable(ctx, record, lease.CaptureFence, err)
	}

	return nil
}

// sealUnprovable decides whether a failure at the seal boundary is terminal.
//
// Two questions, and both must answer yes. Only a typed ErrSealUnconfirmed is a
// statement about the boundary at all -- everything else is a statement about
// the network, and is returned unchanged so the caller retries and this
// coordinator settles nothing and fabricates nothing, which is what it already
// does at every other operation and what this one was the single exception to.
// And only the DEADLINE makes an unproved boundary permanent: requirement 17
// types `seal_unconfirmed` as a drain or container boundary that could not be
// proved *before a database-clock deadline*, so a boundary that cannot be
// proved right now is asked about again on the next pass.
//
// The deadline is read in SQL, from the row, at this moment -- not from
// anything this process composed or remembers.
//
// confirmSeal now asks the same question BEFORE it drives the drain, so this
// arm is what is left over: the deadline that elapses while the boundary is
// being proved. Both readings are the database's, which is the point -- two
// spellings of "past the deadline" would be two answers.
func (coordinator *Coordinator) sealUnprovable(ctx context.Context, record output.HandoffRecord,
	fence output.CaptureFence, cause error) error {
	if !errors.Is(cause, output.ErrSealUnconfirmed) {
		return cause
	}

	passed, err := coordinator.sealDeadlinePassed(ctx, record)
	if err != nil {
		return err
	}
	if !passed {
		return cause
	}

	return coordinator.failTerminally(ctx, record, fence, "seal_unconfirmed")
}

func (coordinator *Coordinator) sealDeadlinePassed(ctx context.Context, record output.HandoffRecord) (bool, error) {
	tx, err := coordinator.Transactor.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	return coordinator.Repository.SealDeadlinePassed(ctx, tx, record.ReservationID)
}

// resolveLogical canonicalizes and commits the logical identity BEFORE any
// object create. Requirement 21.
func (coordinator *Coordinator) resolveLogical(ctx context.Context, record output.HandoffRecord) error {
	if err := requireCaptureAuthority(record, "resolve_logical_reservation"); err != nil {
		return err
	}
	lease, err := coordinator.own(ctx, record)
	if err != nil {
		return err
	}
	control, err := coordinator.control(record)
	if err != nil {
		return err
	}

	canonical, err := control.Canonicalize(ctx,
		coordinator.publicationRequest(record, lease.CaptureFence))
	if err != nil {
		return err
	}

	return coordinator.write(ctx, func(tx Transaction) error {
		return coordinator.Repository.ResolveLogicalReservation(ctx, tx, output.LogicalResolution{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       record.Execution,
			ActivationEpoch: record.ActivationEpoch,
			HandoffID:       record.HandoffID,
			ReservationID:   record.ReservationID,
			CaptureFence:    lease.CaptureFence,
			Scope:           canonical.Scope,
			Digest:          canonical.Digest,
			LogicalBytes:    canonical.LogicalBytes,
			ResolvedAt:      output.NewTimestamp(coordinator.now()),
		})
	})
}

func (coordinator *Coordinator) publicationRequest(record output.HandoffRecord, fence output.CaptureFence) output.PublicationRequest {
	return output.PublicationRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       record.Execution,
		ActivationEpoch: record.ActivationEpoch,
		HandoffID:       record.HandoffID,
		ReservationID:   record.ReservationID,
		CaptureFence:    fence,
	}
}

// publish creates the object and records that a create was attempted.
//
// The record is written AFTER the create returns, and there is no ordering that
// removes the crash between them. What removes the danger is that the create is
// idempotent by identity -- create-if-absent at a server-derived key -- so a
// lost response converges on the next Advance, and the resolution that
// correlates it committed one transition earlier.
func (coordinator *Coordinator) publish(ctx context.Context, record output.HandoffRecord) error {
	if err := requireCaptureAuthority(record, "publish"); err != nil {
		return err
	}
	if !record.LogicalResolved {
		return fmt.Errorf("%w: handoff %s attempted a publish with no committed logical "+
			"resolution", output.ErrUnauthorized, record.HandoffID)
	}
	lease, err := coordinator.own(ctx, record)
	if err != nil {
		return err
	}
	control, err := coordinator.control(record)
	if err != nil {
		return err
	}

	// The point of no return is recorded FIRST, and this is the one place that
	// ordering is deliberate in the other direction: past it, cancellation may
	// no longer release a source, and a create whose response is lost must not
	// leave a canceller believing there is nothing to settle. A row that says
	// "a create was attempted" over an object that was never created is
	// recoverable; an object with no such row is the orphan nothing correlates.
	if err := coordinator.write(ctx, func(tx Transaction) error {
		return coordinator.Repository.RecordFirstObjectCreate(ctx, tx,
			record.ReservationID, lease.CaptureFence)
	}); err != nil {
		return err
	}

	_, err = control.Publish(ctx, coordinator.publicationRequest(record, lease.CaptureFence))

	return err
}

// registerReceipt converges the object, obtains a fresh per-capture receipt
// against a one-use challenge, verifies it and registers it.
//
// The publish is repeated here rather than remembered, and that is what makes
// an ambiguous upload converge: the create is idempotent at a server-derived
// key, so repeating it returns the object that exists -- possibly reporting
// deduplication -- and the generation it reports is the one the challenge names.
func (coordinator *Coordinator) registerReceipt(ctx context.Context, record output.HandoffRecord) error {
	if err := requireCaptureAuthority(record, "register_receipt"); err != nil {
		return err
	}
	lease, err := coordinator.own(ctx, record)
	if err != nil {
		return err
	}
	control, err := coordinator.control(record)
	if err != nil {
		return err
	}

	result, err := control.Publish(ctx, coordinator.publicationRequest(record, lease.CaptureFence))
	if err != nil {
		return err
	}
	if result.Ref.Digest != record.Digest {
		// The object at the derived key is not the tree this reservation
		// resolved. That is a typed collision and never an overwrite.
		//
		// TODO(phase 7, terminal outcomes): this arm cannot fire -- the digest
		// is IN the derived key, so a publish that answered would answer with
		// this reservation's digest or not at all. The real collision signal is
		// the publisher's typed ErrConflict, which arrives from the call above
		// and is retried on every pass: three passes, three identical refusals,
		// nothing registered, the source held. That is the honest answer for an
		// out-of-band writer -- the bytes may be put back -- but it is unbounded
		// until the capture deadline, and nothing enforces `capture_deadline_at`
		// yet. Phase 7 owns both: the enforcement, and the decision that a
		// collision at a server-derived key becomes terminal at it. Round-2
		// review finding R2-F6.
		return coordinator.failTerminally(ctx, record, lease.CaptureFence, "collision")
	}

	var challenge output.StatChallenge
	if err := coordinator.write(ctx, func(tx Transaction) error {
		var err error
		challenge, err = coordinator.Repository.IssueStatChallenge(ctx, tx,
			record.HandoffID, record.ReservationID, result.Ref, lease.CaptureFence,
			coordinator.ReceiptKeyID, coordinator.challengeTerm())

		return err
	}); err != nil {
		return err
	}

	receipt, err := control.Attest(ctx, challenge, output.ReceiptClaims{
		Execution:            record.Execution,
		ProducerCheckpointID: record.ProducerCheckpointID,
		Incarnation:          record.Source.Incarnation,
		Output:               record.Output,
		WriterFence:          output.WriterFence(lease.CaptureFence),
	})
	if err != nil {
		return err
	}

	// Verified with the production verifier against the activation epoch and
	// the challenge, before anything is registered. A syntactically valid
	// receipt is not evidence; a signature over the challenge this transaction
	// is about to consume is.
	if err := coordinator.Verifier.Verify(receipt, challenge); err != nil {
		return err
	}
	// And every signed claim matched against the durable state, which is the
	// half only the control plane can do: the daemon signed what it observed,
	// and whether what it observed is THIS capture's checkpoint, incarnation,
	// output and fence is a question about rows.
	if err := checkReceiptClaims(receipt, record, lease.CaptureFence); err != nil {
		return err
	}

	// The receipt and the sentence that tells a watcher the capture succeeded
	// are one commit. This is the transaction whose answer P4 loses, and an
	// announcement made after it is an announcement nothing ever makes again:
	// the capture is registered, the transition is never re-taken, and the
	// recovery component does not revisit a handoff that has settled.
	return coordinator.write(ctx, func(tx Transaction) error {
		if err := coordinator.Repository.RegisterReceipt(ctx, tx, output.ReceiptAdmission{
			ProtocolVersion: output.ProtocolVersion,
			Receipt:         receipt,
			ChallengeNonce:  challenge.Nonce,
			Metageneration:  result.Metageneration,
			AdmittedAt:      output.NewTimestamp(coordinator.now()),
		}); err != nil {
			return err
		}

		return coordinator.say(ctx, tx, record, coordinator.announce(AnnouncementDisposition,
			output.DispositionCapture, "captured"))
	})
}

// checkReceiptClaims matches a receipt's signed claims to the durable record.
//
// Requirement 26: syntactic validity and a caller-provided reference are not
// enough. A signature proves the daemon said it; this proves the daemon said it
// about THIS capture. Every field here is one a replayed receipt from another
// capture, source, output or fence would differ in, which is the whole reason
// the list is long rather than a spot check.
func checkReceiptClaims(receipt output.Receipt, record output.HandoffRecord, fence output.CaptureFence) error {
	claims := receipt.Claims

	switch {
	case claims.Execution != record.Execution:
		return fmt.Errorf("%w: the receipt is for execution %s and this capture is %s",
			output.ErrInvalidIdentity, claims.Execution.ExecutionID, record.Execution.ExecutionID)
	case claims.ProducerCheckpointID != record.ProducerCheckpointID:
		return fmt.Errorf("%w: the receipt names another producer checkpoint",
			output.ErrInvalidIdentity)
	case claims.Incarnation != record.Source.Incarnation:
		return fmt.Errorf("%w: the receipt names another source incarnation",
			output.ErrInvalidIdentity)
	case claims.Output != record.Output:
		return fmt.Errorf("%w: the receipt names output %q and this capture selected %q",
			output.ErrInvalidIdentity, claims.Output, record.Output)
	case claims.Ref.Scope != record.Scope || claims.Ref.Digest != record.Digest:
		return fmt.Errorf("%w: the receipt is for %s and the reservation resolved %s",
			output.ErrInvalidIdentity, claims.Ref.Digest, record.Digest)
	case claims.ActivationEpoch != record.ActivationEpoch:
		return fmt.Errorf("%w: the receipt is signed under epoch %d and this capture is admitted "+
			"under %d", output.ErrInvalidIdentity, claims.ActivationEpoch, record.ActivationEpoch)
	case claims.WriterFence != output.WriterFence(fence):
		return fmt.Errorf("%w: the receipt is bound to fence %d and this owner holds %d",
			output.ErrInvalidIdentity, claims.WriterFence, fence)
	}

	return nil
}

// settleOrphan is the only thing left past the irreversible publish point, and
// it has nothing to settle INTO yet.
//
// It used to call the generic cancel/settle seam, which returns early past the
// publish point -- so the transition settled nothing and answered nil, and a
// recovery pass walked away believing it had done something. There is no
// terminal orphan state in the schema either: `settlement_is_earned` needs a
// release and the release guard refuses one past the publish point, so there is
// no row this could write even if it wanted to.
//
// It refuses, before touching anything. The state is unreachable from this
// coordinator today -- cancellation past the point is routed to
// register_receipt, and the collision arm cannot fire because the digest is in
// the key -- and that is the argument FOR the refusal rather than against it:
// an unreachable no-op is invisible forever, and an unreachable refusal is a
// message the day something reaches it.
//
// TODO(phase 7, reclamation): the terminal orphan outcome is a durable state --
// a created object that no receipt correlates, held as inventory debt rather
// than settled away -- and this becomes the transition that records it.
func (coordinator *Coordinator) settleOrphan(_ context.Context, record output.HandoffRecord) error {
	return fmt.Errorf("%w: handoff %s is %s past the irreversible publish point, and a terminal "+
		"orphan has no durable outcome to settle into yet. An object may exist for it and no "+
		"receipt correlates one; that is inventory debt, and recording it is reclamation's",
		output.ErrIncomplete, record.HandoffID, record.State)
}

// failTerminally commits a typed failure and announces it.
//
// It never creates a receipt or a claim, and the release the failure then owes
// is a later transition rather than part of this one -- the source is on a node
// and only that node can say it is gone.
func (coordinator *Coordinator) failTerminally(ctx context.Context, record output.HandoffRecord, fence output.CaptureFence, failure string) error {
	return coordinator.write(ctx, func(tx Transaction) error {
		if err := coordinator.Repository.RecordTerminalCaptureFailure(ctx, tx,
			record.ReservationID, fence, failure); err != nil {
			return err
		}

		return coordinator.say(ctx, tx, record,
			coordinator.announce(AnnouncementDisposition, output.DispositionCapture, failure))
	})
}

// say emits one announcement INSIDE the caller's transaction, and a deployment
// with no announcer is silent rather than broken.
//
// Every caller here is the transaction that commits the fact being announced.
// That is the rule: a disposition and the sentence that tells a watcher about
// it commit together or neither commits, so no lost answer can leave the plane
// holding a terminal outcome nobody was told about.
func (coordinator *Coordinator) say(ctx context.Context, tx output.Tx,
	record output.HandoffRecord, announcement Announcement) error {
	if coordinator.Announcer == nil {
		return nil
	}
	if err := announcement.Validate(); err != nil {
		return err
	}

	return coordinator.Announcer.Announce(ctx, tx, record.HandoffID, announcement)
}

// TerminalOutcome reports the settled disposition and its reason for a handoff,
// for a caller that has been told exposure is permitted.
//
// It answers WHAT happened and never what to do about it: capture success
// permits ordinary success, a terminal capture failure fails the step and a
// completed no_capture preserves the authoritative ordinary non-success, and
// all three are the consumer's to apply. A product-neutral plane that returned
// a step result would be choosing one.
func TerminalOutcome(record output.HandoffRecord) (output.Disposition, string) {
	if record.Disposition == nil {
		return "", ""
	}

	return *record.Disposition, terminalReason(record)
}
