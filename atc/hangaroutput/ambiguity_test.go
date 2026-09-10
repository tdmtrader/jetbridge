package hangaroutput_test

// What a lost answer costs, at every operation that can lose one.
//
// The five assertions the plan asks for run through every spec here, and they
// are asserted as OUTCOMES rather than as call counts wherever an outcome
// exists: the source is still on the node, no object is in the bucket, no
// receipt row is registered, no terminal outcome is exposed. Where a count is
// the assertion -- "the producer command ran once" -- it is a count of what the
// daemon's own ledger recorded, not of what a double was asked.
//
// Reqs 4-11, 17, 21-27; ACs 2, 6, 9, 14.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// capture is one admitted capture, at whatever stage the spec drove it to.
type capture struct {
	harness   *harness
	Handoff   output.HandoffID
	Lease     output.SourceLeaseID
	Execution executioncontrol.Identity
	Output    output.OutputName
	Reserved  output.ReservedIncarnation
	Deadline  output.Timestamp
}

// admit takes a capture through everything that happens BEFORE a finish: the
// predeclaration, the daemon's reservation, the producing pod's hold, and the
// bytes a producer writes. Every step of it goes through production.
func (h *harness) admit(t *testing.T) *capture {
	t.Helper()

	ctx := context.Background()
	admitted := &capture{
		harness:   h,
		Handoff:   output.HandoffID(uuid.NewString()),
		Lease:     output.SourceLeaseID(uuid.NewString()),
		Execution: executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1},
		Output:    "result",
		Deadline:  output.NewTimestamp(time.Now().Add(24 * time.Hour)),
	}

	if _, err := h.Daemon.Client.Admit(ctx, executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        admitted.Execution,
		ActivationEpoch: harnessEpoch,
		NodeUID:         harnessNode,
		Capability:      "opaque-capability",
	}); err != nil {
		t.Fatalf("admitting the execution: %v", err)
	}

	admission := output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       admitted.Execution,
		ActivationEpoch: harnessEpoch,
		HandoffID:       admitted.Handoff,
		SourceLeaseID:   admitted.Lease,
		Output:          admitted.Output,
		CaptureDeadline: admitted.Deadline,
	}

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.PredeclareHandoff(ctx, tx, admission); err != nil {
		t.Fatalf("predeclaring: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The reservation comes from the daemon, before any Pod exists, and the
	// control plane records where it is. Nothing here composes a path.
	reserved, err := h.Daemon.Client.ReserveIncarnation(ctx, admission)
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	admitted.Reserved = reserved

	tx, err = h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.RecordSourceReservation(ctx, tx, reserved, "harness-node"); err != nil {
		t.Fatalf("recording the reservation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return admitted
}

// hold is the capture control init's half: the producing Pod exists now, and
// the hold binds to the reserved incarnation and to that Pod.
func (c *capture) hold(t *testing.T) *capture {
	t.Helper()

	ctx := context.Background()
	pod := executioncontrol.PodUID(uuid.NewString())

	if _, err := c.harness.Daemon.Client.RecordStart(ctx, c.Execution, pod, "producer-1"); err != nil {
		t.Fatalf("recording the start: %v", err)
	}

	ack := c.harness.Daemon.holdSource(t, output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       c.Execution,
		ActivationEpoch: harnessEpoch,
		HandoffID:       c.Handoff,
		SourceLeaseID:   c.Lease,
		Output:          c.Output,
		CaptureDeadline: c.Deadline,
	}, c.Reserved.Incarnation, pod)

	tx, err := c.harness.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := c.harness.Repository.AcknowledgeSourceHold(ctx, tx, ack); err != nil {
		t.Fatalf("acknowledging the hold: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The bytes a producer wrote, into the directory its hold protects.
	if err := os.WriteFile(filepath.Join(c.harness.Daemon.StepsDir,
		c.Reserved.Directory, "artifact.txt"),
		[]byte("the bytes a producer wrote\n"), 0o600); err != nil {
		t.Fatalf("writing the produced source: %v", err)
	}

	return c
}

// finish records the exact finish witness at the daemon and hands it to the
// control plane, which is the fact Stage 2 is admitted on.
func (c *capture) finish(t *testing.T, successful bool) *capture {
	t.Helper()

	code := 0
	if !successful {
		code = 2
	}
	if _, err := c.harness.Daemon.Client.RecordOutcome(context.Background(), c.Execution,
		executioncontrol.AcknowledgementFinish,
		executioncontrol.ExitOutcome{ExitCode: code}); err != nil {
		t.Fatalf("recording the outcome: %v", err)
	}

	return c
}

// advance runs the coordinator until it says there is nothing left, or until it
// stops making progress.
//
// It is bounded, and the bound is the assertion: a coordinator that made no
// progress would spin, and a spec that looped forever would report as a hang
// rather than as a failure.
func (c *capture) advance(t *testing.T) []hangaroutput.Transition {
	t.Helper()

	var taken []hangaroutput.Transition
	for i := 0; i < 12; i++ {
		decision, err := c.harness.Coordinator.Advance(context.Background(), c.Handoff)
		taken = append(taken, decision.Transition)
		if err != nil {
			t.Fatalf("advancing (%s) after %v: %v", decision.Transition, taken, err)
		}
		if decision.Transition == hangaroutput.TransitionNone ||
			decision.Transition == hangaroutput.TransitionAwaitOutcome {
			return taken
		}
	}
	t.Fatalf("the coordinator took 12 transitions without settling: %v", taken)

	return taken
}

// advanceOnce runs exactly one transition and returns what happened, error and
// all, because half these specs are about what the error was.
func (c *capture) advanceOnce(t *testing.T) (hangaroutput.Decision, error) {
	t.Helper()

	return c.harness.Coordinator.Advance(context.Background(), c.Handoff)
}

func (c *capture) record(t *testing.T) output.HandoffRecord {
	t.Helper()

	tx, err := c.harness.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	record, err := c.harness.Repository.LoadHandoffRecord(context.Background(), tx, c.Handoff)
	if err != nil {
		t.Fatalf("loading: %v", err)
	}

	return record
}

// sourceStillThere reads the NODE, not a call count. "The daemon was not asked
// to release" is unassertable; "the bytes are still on the node" is a fact.
func (c *capture) sourceStillThere() bool {
	_, err := os.Stat(filepath.Join(c.harness.Daemon.StepsDir, c.Reserved.Directory))

	return err == nil
}

// expireTheLease moves the capture ownership lease wholesale into the past.
//
// Expiry alone releases nothing and proves nothing -- what it permits is a
// takeover, and the takeover advances the fence. The row is moved rather than
// shortened because the schema refuses a term under fifteen minutes: what is
// being simulated is time passing, not an illegally short lease.
func (c *capture) expireTheLease(t *testing.T) {
	t.Helper()

	if _, err := c.harness.Conn.Exec(`
		UPDATE hangar_capture_attempt_leases
		SET renewed_at = now() - interval '2 hours', expires_at = now() - interval '1 hour'
		WHERE reservation_id = $1`, string(c.record(t).ReservationID)); err != nil {
		t.Fatalf("expiring the lease: %v", err)
	}
}

func (h *harness) bucketKeys(t *testing.T) []string {
	t.Helper()

	objects, _, err := h.Store.ListObjects(h.Bucket, "", "", false)
	if err != nil {
		t.Fatalf("listing the bucket: %v", err)
	}
	var keys []string
	for _, object := range objects {
		keys = append(keys, object.Name)
	}

	return keys
}

// The control, and it is asserted before every injection below: with nothing
// injected the whole capture completes, one object exists, a receipt is
// registered, the source is released and a terminal outcome is permitted.
//
// Without it every "no object was created" and "no receipt was registered"
// assertion in this file would pass on a coordinator that does nothing at all.
func TestAnUninterruptedCaptureCompletesAndReleasesItsSource(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	if permitted, _ := hangaroutput.TerminalExposurePermitted(c.record(t)); permitted {
		t.Error("a terminal outcome was permitted before the handoff resolved")
	}

	taken := c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateRegistered {
		t.Fatalf("the capture is %s after %v", record.State, taken)
	}
	if record.Receipt == nil {
		t.Error("no receipt was registered")
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the bucket holds %d object(s): %v", len(keys), keys)
	}
	if permitted, reason := hangaroutput.TerminalExposurePermitted(record); !permitted {
		t.Errorf("a completed capture does not permit a terminal outcome: %s", reason)
	}
	disposition, reason := hangaroutput.TerminalOutcome(record)
	if disposition != output.DispositionCapture || reason != "captured" {
		t.Errorf("the outcome is %s/%s", disposition, reason)
	}

	// Requirement 18 reached a watcher. WHICH announcements and in what order
	// is hangar-disposition.feature's assertion, over a real build's own event
	// stream; what is asserted here is only that the coordinator emitted at
	// all, so that a silent emitter is not discovered three phases later.
	if len(h.Announcer.Kinds()) == 0 {
		t.Error("a completed capture told a watcher nothing at all")
	}
}

// An ambiguous PostgreSQL commit at Stage 2: the rows are there and the caller
// does not know it.
//
// The repeat has to be idempotent on the same identity, which is only true if
// the producer checkpoint id is DERIVED rather than minted. A fresh id on the
// retry is a different Stage 2 wearing an old idempotency key, and the
// repository correctly refuses it -- forever.
func TestAnAmbiguousStageTwoCommitConvergesOnTheSameCheckpoint(t *testing.T) {
	h := newHarness(t)
	ambiguous := &ambiguousTransactor{inner: h.Coordinator.Transactor}
	h.Coordinator.Transactor = ambiguous

	c := h.admit(t).hold(t).finish(t, true)

	ambiguous.LoseNext = true
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost commit was not reported: %v", err)
	}

	// It DID commit. The next read sees it, and nothing re-decides the branch.
	record := c.record(t)
	if record.Disposition == nil || *record.Disposition != output.DispositionCapture {
		t.Fatalf("the ambiguous commit left the arbiter %v", record.Disposition)
	}
	if !record.HasCaptureReservation() {
		t.Fatal("the arbiter won capture with no reservation row")
	}
	if h.bucketKeys(t) != nil {
		t.Error("an object was created before the reservation resolved")
	}

	c.advance(t)
	if final := c.record(t); final.State != output.CaptureStateRegistered {
		t.Errorf("the capture never converged: %s", final.State)
	}
}

// A daemon timeout at the seal: the seal BEGAN and the answer was lost.
//
// The seal is the node's fact, so recovery asks the node rather than assuming.
// A coordinator that treated the lost answer as "no seal" would begin a second
// seal; one that treated it as done would confirm a drain it never captured.
func TestALostSealAnswerIsResolvedByAskingTheNode(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	if _, err := c.advanceOnce(t); err != nil {
		t.Fatalf("Stage 2: %v", err)
	}

	h.Dialer.LoseAfter = "begin-seal"
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost seal answer was not reported: %v", err)
	}
	if !c.sourceStillThere() {
		t.Error("the source is gone after a lost seal answer")
	}
	if h.bucketKeys(t) != nil {
		t.Error("an object was created while the seal was unresolved")
	}

	c.advance(t)
	if h.Dialer.Calls("begin-seal") != 1 {
		t.Errorf("the seal was begun %d times; recovery asked the node instead of assuming",
			h.Dialer.Calls("begin-seal"))
	}
	if final := c.record(t); final.State != output.CaptureStateRegistered {
		t.Errorf("the capture never converged: %s", final.State)
	}
}

// A daemon RESTART between the seal and the canonicalization.
//
// The ledger is durable, so the restart changes nothing a coordinator can see.
// This is the spec that would fail if the seal lived in memory.
func TestADaemonRestartBetweenSealingAndPublishingChangesNothing(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2, seal, confirm.
	for i := 0; i < 3; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}

	h.Daemon.Restart(t)

	c.advance(t)
	record := c.record(t)
	if record.State != output.CaptureStateRegistered {
		t.Errorf("the capture is %s after a daemon restart", record.State)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the bucket holds %d object(s): %v", len(keys), keys)
	}
}

// A Kubernetes timeout at the drain boundary: no complete final container
// status before the deadline.
//
// Requirement 17: that is typed `seal_unconfirmed`, it publishes no receipt,
// and it does not re-execute anything. What it DOES owe is a fenced release --
// the source is still on a node -- and the handoff is not settled until that
// release is acknowledged.
func TestAnUnprovableDrainIsSealUnconfirmedAndPublishesNothing(t *testing.T) {
	h := newHarness(t)
	h.Drain.Unprovable = true

	c := h.admit(t).hold(t).finish(t, true)
	c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateFailed {
		t.Fatalf("an unprovable drain left the capture %s", record.State)
	}
	if record.TerminalFailure != "seal_unconfirmed" {
		t.Errorf("the terminal failure is %q", record.TerminalFailure)
	}
	if record.Receipt != nil {
		t.Error("an unconfirmed seal produced a receipt")
	}
	if h.bucketKeys(t) != nil {
		t.Error("an unconfirmed seal created an object")
	}
	if record.LogicalResolved {
		t.Error("an unconfirmed seal resolved a logical identity; no canonical read may begin")
	}
	if !record.ReleaseAcknowledged {
		t.Error("the source was never released after a terminal failure")
	}
	if c.sourceStillThere() {
		t.Error("the released incarnation is still on the node")
	}
}

// A LOST SEAL CONFIRMATION. The daemon confirmed the seal and the answer never
// arrived.
//
// This is the one boundary in the coordinator where a lost answer used to be
// GUESSED rather than repeated: any error at all from the drain confirmer or
// the confirm-seal call was committed as terminal `seal_unconfirmed`, and the
// release that failure then owes destroyed a healthy, fully produced output --
// while the daemon's own record said `confirmed`. One dropped HTTP response
// failed the step.
//
// Req 5: an ambiguous acknowledgement is repeated under the same identity, and
// the source is preserved. Only a TYPED ErrSealUnconfirmed is a statement about
// the boundary; anything else is a statement about the network.
func TestALostSealConfirmationConvergesOnTheNode(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal.
	for i := 0; i < 2; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}

	h.Dialer.LoseAfter = "confirm-seal"
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost seal confirmation was not reported as ambiguous: %v", err)
	}

	// Nothing was decided on it, and nothing was destroyed by it.
	interim := c.record(t)
	if interim.State == output.CaptureStateFailed {
		t.Fatalf("a lost confirm-seal answer was committed as terminal %q while the daemon's own "+
			"record says the seal is confirmed", interim.TerminalFailure)
	}
	if !c.sourceStillThere() {
		t.Error("the source was released on a lost answer")
	}

	c.advance(t)

	final := c.record(t)
	if final.State != output.CaptureStateRegistered {
		t.Fatalf("the capture is %s/%q; a healthy sealed source lost one answer",
			final.State, final.TerminalFailure)
	}
	if final.Receipt == nil {
		t.Error("no receipt was registered")
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the bucket holds %d object(s): %v", len(keys), keys)
	}
	if h.Dialer.Calls("confirm-seal") < 1 {
		t.Error("the seal was never confirmed at the node at all")
	}
}

// A TRANSIENT drain error -- a kube-apiserver that did not answer in time.
//
// It is not evidence about the container boundary and it must not be committed
// as one. The next pass asks again, and the capture completes.
func TestATransientDrainErrorIsRetriedRatherThanCommitted(t *testing.T) {
	h := newHarness(t)
	h.Drain.TransientErrors = 1

	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal, then the confirmation that cannot reach the
	// apiserver.
	for i := 0; i < 2; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}
	if _, err := c.advanceOnce(t); !errors.Is(err, output.ErrInfrastructure) {
		t.Fatalf("a transient drain error was not reported as one: %v", err)
	}

	interim := c.record(t)
	if interim.State == output.CaptureStateFailed {
		t.Fatalf("one unreachable apiserver was committed as terminal %q", interim.TerminalFailure)
	}
	if !c.sourceStillThere() {
		t.Error("the source was released on a transient error")
	}

	c.advance(t)
	if final := c.record(t); final.State != output.CaptureStateRegistered {
		t.Errorf("the capture is %s/%q after the apiserver came back",
			final.State, final.TerminalFailure)
	}
}

// A lost UPLOAD response. The object exists; the caller does not know it.
//
// AC 9: an ambiguous create converges only through verified per-capture retry.
// The retry repeats the same identity at a server-derived key, so the store
// answers with the object that is already there -- one object, one generation,
// one receipt.
func TestALostUploadResponseConvergesOnOneObjectAndOneReceipt(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2, seal, confirm, resolve.
	for i := 0; i < 4; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}

	h.Dialer.LoseAfter = "publish"
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost upload response was not reported: %v", err)
	}

	// The reservation resolved BEFORE the create, which is the whole point of
	// requirement 21: the object that may now exist is correlatable.
	interim := c.record(t)
	if !interim.LogicalResolved {
		t.Fatal("an object create was attempted with no committed logical resolution")
	}
	if !interim.PastIrreversiblePublishPoint {
		t.Error("the create attempt was not recorded, so a canceller would think it could release")
	}
	if interim.Receipt != nil {
		t.Error("a receipt was registered from an unresolved upload")
	}

	c.advance(t)

	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the retry created %d objects: %v", len(keys), keys)
	}
	final := c.record(t)
	if final.Receipt == nil {
		t.Fatal("the capture never registered a receipt")
	}
	if final.Ref.Digest != interim.Digest {
		t.Errorf("the registered ref is %s and the resolution named %s",
			final.Ref.Digest, interim.Digest)
	}
}

// A lost RELEASE acknowledgement, on the no_capture branch.
//
// The source is gone at the node and the caller does not know it. The repeat
// has to name the SAME intent, which is why the intent id is derived: a fresh
// one would be a second release of one source, which the daemon refuses, and
// the handoff would never complete.
func TestALostReleaseAcknowledgementIsRepeatedUnderTheSameIntent(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, false)

	if _, err := c.advanceOnce(t); err != nil {
		t.Fatalf("recording no_capture: %v", err)
	}

	// Between the halves: the outcome is pending, the source is held, and no
	// terminal outcome may be exposed.
	between := c.record(t)
	if between.Settled {
		t.Error("no_capture settled before its release was acknowledged")
	}
	if !c.sourceStillThere() {
		t.Error("the source was released before the disposition's second half")
	}
	if permitted, _ := hangaroutput.TerminalExposurePermitted(between); permitted {
		t.Error("a terminal outcome was permitted between the two halves")
	}

	h.Dialer.LoseAfter = "release"
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost release answer was not reported: %v", err)
	}

	c.advance(t)
	final := c.record(t)
	if !final.ReleaseAcknowledged {
		t.Fatal("the release was never acknowledged, so the handoff never completed")
	}
	if final.Receipt != nil {
		t.Error("no_capture registered a receipt")
	}
	if h.bucketKeys(t) != nil {
		t.Error("no_capture created an object")
	}
	if permitted, reason := hangaroutput.TerminalExposurePermitted(final); !permitted {
		t.Errorf("a settled no_capture does not permit a terminal outcome: %s", reason)
	}
}

// NODE LOSS: the node is gone and stays gone.
//
// It is the injection whose correct answer is to do nothing, and that is what
// makes it worth a spec: a coordinator that reported the source released, or
// that fabricated a receipt, would be inventing the one fact only a node can
// state. Nothing is settled, nothing is terminal, and the outcome the caller
// gets is a refusal it can retry.
func TestALostNodeSettlesNothingAndFabricatesNothing(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	if _, err := c.advanceOnce(t); err != nil {
		t.Fatalf("Stage 2: %v", err)
	}

	h.Dialer.Unreachable = true
	if _, err := c.advanceOnce(t); err == nil {
		t.Fatal("a lost node was reported as progress")
	}

	record := c.record(t)
	if record.Settled {
		t.Error("a handoff whose node is gone reported itself settled")
	}
	if record.Receipt != nil {
		t.Error("a lost node produced a receipt")
	}
	if h.bucketKeys(t) != nil {
		t.Error("a lost node created an object")
	}
	if permitted, _ := hangaroutput.TerminalExposurePermitted(record); permitted {
		t.Error("a terminal outcome was permitted while a handoff's node was unreachable")
	}
}

// A STALE OWNER: the lease was taken over between two transitions.
//
// Every capture transition takes the lease first, so the takeover is caught at
// the top of the step rather than three calls later. The stale owner may not
// resolve, create, sign, register, finalize or release, and the assertion is
// that it did none of them.
func TestAStaleOwnerCannotAdvanceACapture(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal, so that the coordinator really owns a lease. A
	// takeover of a lease nobody holds is not a takeover.
	for i := 0; i < 2; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}
	record := c.record(t)

	c.expireTheLease(t)

	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	taken, err := h.Repository.AcquireCaptureLease(context.Background(), tx,
		record.ReservationID, uuid.NewString(), time.Hour)
	if err != nil {
		t.Fatalf("taking over: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if taken.CaptureFence <= 1 {
		t.Fatalf("a takeover did not advance the fence: %d", taken.CaptureFence)
	}

	// The old owner is refused, and refused at the LEASE rather than after a
	// publication it had no right to make.
	if _, err := c.advanceOnce(t); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a stale owner advanced the capture: %v", err)
	}
	if h.bucketKeys(t) != nil {
		t.Error("a stale owner created an object")
	}
	if after := c.record(t); after.Receipt != nil {
		t.Error("a stale owner registered a receipt")
	}
}

// The POSITIVE half of a lease takeover: the new owner finishes what it
// inherited.
//
// A takeover is what an ATC restart mid-capture becomes, because OwnerID is
// minted per process: after DefaultLeaseTerm the next process is a different
// owner and its first `own` advances the fence. The spec above proves the OLD
// owner is refused; this one proves the NEW one is not, which is the half
// recovery depends on and the half that was broken -- two writes checked the
// reservation ROW's fence, frozen at 1 by Stage 2, while everything else
// checked the lease, so every capture that survived a restart past Stage 2
// ended as an unregistered object nobody could either register or fail.
func TestATakeoverCarriesTheCaptureThroughToRegistration(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal, under the first owner. A takeover of a lease
	// nobody holds is not a takeover.
	for i := 0; i < 2; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}
	c.expireTheLease(t)

	// The second process. A new owner id is exactly what an ATC restart
	// produces, and it is the whole difference between the two owners.
	h.Coordinator.OwnerID = uuid.NewString()

	taken := c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateRegistered {
		t.Fatalf("a capture inherited by a new owner is %s after %v", record.State, taken)
	}
	if record.CaptureFence != 2 {
		t.Errorf("the capture is admitted under fence %d and the takeover advanced the lease "+
			"to 2; one fence source or none", record.CaptureFence)
	}
	if record.Receipt == nil {
		t.Fatal("the new owner published an object and could never obtain a receipt for it")
	}
	if record.Receipt.Claims.WriterFence != output.WriterFence(2) {
		t.Errorf("the receipt is bound to writer fence %d and the takeover holds 2",
			record.Receipt.Claims.WriterFence)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the takeover created %d object(s): %v", len(keys), keys)
	}
	if h.Dialer.Calls("begin-seal") != 1 {
		t.Errorf("the seal was begun %d times across the takeover; the captured drain set is "+
			"captured once", h.Dialer.Calls("begin-seal"))
	}
	if permitted, reason := hangaroutput.TerminalExposurePermitted(record); !permitted {
		t.Errorf("a capture completed by its new owner does not permit a terminal outcome: %s",
			reason)
	}
}

// The producer is never re-executed, at any of these boundaries.
//
// The count is the DAEMON's own record of starts, not a double's. Requirement
// 6: from exact process start onward, recovery never re-executes or recreates
// that producer command.
func TestNoInjectionEverReExecutesTheProducer(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	h.Dialer.LoseAfter = "canonicalize"
	for i := 0; i < 8; i++ {
		if _, err := c.advanceOnce(t); err != nil && !errors.Is(err, lostAnswer) {
			t.Fatalf("advancing: %v", err)
		}
	}

	observed, err := h.Daemon.Client.Observe(context.Background(), c.Execution, 0)
	if err != nil {
		t.Fatalf("observing: %v", err)
	}
	if observed.Acknowledgement == nil ||
		observed.Acknowledgement.Kind != executioncontrol.AcknowledgementFinish {
		t.Fatalf("the execution has no finish acknowledgement: %+v", observed.Acknowledgement)
	}
	if observed.Acknowledgement.ProcessIdentity != "producer-1" {
		t.Errorf("the execution reports process %q; a second producer ran",
			observed.Acknowledgement.ProcessIdentity)
	}
}
