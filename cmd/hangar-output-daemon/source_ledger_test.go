package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/ledger"
)

// The source ledger is the node-local authority for one source incarnation:
// which bytes a capture will seal, who may write to them, and when nobody may
// any more. It is the half of a capture that no database can own, because the
// bytes are on this node's disk and the question "is anybody still writing"
// has no answer anywhere else.
//
// Real directories again, for the same reason the execution ledger uses them:
// half of what an operation here does is a change to a filesystem that no
// response shows.

const (
	testHandoff  = output.HandoffID("11111111-1111-4111-8111-111111111111")
	testLease    = output.SourceLeaseID("22222222-2222-4222-8222-222222222222")
	testTicket   = output.WriterTicketID("66666666-6666-4666-8666-666666666666")
	testTicketB  = output.WriterTicketID("77777777-7777-4777-8777-777777777777")
	testIntent   = output.ReleaseIntentID("88888888-8888-4888-8888-888888888888")
	testOutput   = output.OutputName("result")
	captureFence = output.CaptureFence(5)
)

type sourceFixture struct {
	ledgerFixture

	source *SourceLedger
}

func newSourceLedger(t *testing.T) *sourceFixture {
	t.Helper()

	fixture := &sourceFixture{ledgerFixture: *newLedger(t)}
	fixture.openSource(t)

	return fixture
}

func (fixture *sourceFixture) openSource(t *testing.T) {
	t.Helper()

	signer, err := output.NewCaptureStatementSigner(fixture.private)
	if err != nil {
		t.Fatalf("building the capture signer: %v", err)
	}
	source, err := OpenSourceLedger(fixture.store, fixture.ledger, testNode, testEpoch,
		signer, fixture.clock, filepath.Join(fixture.dir, "steps"))
	if err != nil {
		t.Fatalf("opening the source ledger: %v", err)
	}
	fixture.source = source
}

func (fixture *sourceFixture) restart(t *testing.T) {
	t.Helper()

	fixture.ledgerFixture.reopen(t)
	fixture.openSource(t)
}

func admission() output.CaptureAdmission {
	return output.CaptureAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		SourceLeaseID:   testLease,
		Output:          testOutput,
		CaptureDeadline: output.NewTimestamp(fixedNow().Add(time.Hour)),
	}
}

// held is the state every later assertion starts from: an admitted execution
// whose incarnation the daemon has RESERVED and whose source it then holds.
//
// The reservation is part of the state now, and not an extra step this helper
// happens to take. The incarnation is issued before the Pod exists so the ATC
// can mount it as the producer's output volume; a hold is bound to it, and a
// fixture that skipped the reservation would be exercising a path production
// no longer has.
func held(t *testing.T, fixture *sourceFixture) output.CaptureAcknowledgement {
	t.Helper()

	admitted(t, &fixture.ledgerFixture)

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving the incarnation: %v", err)
	}

	ack, err := fixture.source.AcknowledgeHold(context.Background(), admission(), reserved.Incarnation, testPod)
	if err != nil {
		t.Fatalf("holding the source: %v", err)
	}

	return ack
}

// The hold is server-issued, pre-start and non-authorizing.
func TestTheHoldIsAcknowledgedForAnIncarnationTheServerIssued(t *testing.T) {
	fixture := newSourceLedger(t)
	ack := held(t, fixture)

	if err := ack.ValidateAs(output.CaptureHoldAcknowledged); err != nil {
		t.Fatalf("the hold statement is not a hold: %v", err)
	}
	if err := output.VerifyCaptureAcknowledgement(ack, fixture.public); err != nil {
		t.Errorf("the hold statement does not verify: %v", err)
	}

	// The incarnation is the SERVER's. A caller offering one is offering a name
	// it chose, and Req 7 says a handle string alone is never an identity.
	if ack.Incarnation.HandleGeneration == 0 {
		t.Error("the daemon issued no handle generation")
	}
	if ack.Incarnation.NodeUID != testNode {
		t.Errorf("the incarnation names node %s", ack.Incarnation.NodeUID)
	}
	if ack.Incarnation.Output != testOutput {
		t.Errorf("the incarnation names output %s", ack.Incarnation.Output)
	}
	// The pre-start hold authorizes no writing at all.
	if ack.WriterFence != 0 || ack.WriterTicketID != "" {
		t.Errorf("the pre-start hold carries writer fence %d and ticket %q",
			ack.WriterFence, ack.WriterTicketID)
	}

	// And the source is really on the node, under the daemon's own root.
	if !fixture.source.Holds(ack.Incarnation) {
		t.Error("the daemon acknowledged a hold and holds nothing")
	}

	// The hold is a gate on the base execution: cleanup cannot proceed while it
	// is open, and the base ledger says so without learning what a hold is.
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	eligible, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking: %v", err)
	}
	if eligible.Eligible {
		t.Error("cleanup was permitted while the source was still held")
	}
}

// Idempotent replay and its conflict twin, on the hold.
//
// "Repeating the handoff returns the same state" passes for a daemon that
// ignores the identity entirely, so the twin has to show that reuse for
// DIFFERENT facts is a typed conflict and the original hold still stands.
func TestARepeatedHoldReturnsTheSameStatementAndADifferentFenceIsAConflict(t *testing.T) {
	fixture := newSourceLedger(t)
	first := held(t, fixture)

	again, err := fixture.source.AcknowledgeHold(context.Background(), admission(), first.Incarnation, testPod)
	if err != nil {
		t.Fatalf("repeating the hold: %v", err)
	}
	if !sameCaptureStatement(again, first) {
		t.Errorf("the repeat returned a different statement:\n first: %+v\nrepeat: %+v", first, again)
	}

	for name, mutate := range map[string]func(*output.CaptureAdmission){
		"a different execution fence": func(a *output.CaptureAdmission) { a.Execution.Fence = 2 },
		"a different source lease":    func(a *output.CaptureAdmission) { a.SourceLeaseID = "99999999-9999-4999-8999-999999999999" },
		"a different output":          func(a *output.CaptureAdmission) { a.Output = "other" },
	} {
		different := admission()
		mutate(&different)

		if _, err := fixture.source.AcknowledgeHold(context.Background(), different, first.Incarnation, testPod); !errors.Is(err, output.ErrConflict) {
			t.Errorf("a hold repeated with %s was not a typed conflict: %v", name, err)
		}
	}

	// And the first hold is still the one in force.
	current, err := fixture.source.InspectHold(testHandoff, identity(1))
	if err != nil {
		t.Fatalf("inspecting: %v", err)
	}
	if !sameCaptureStatement(current, first) {
		t.Error("a refused conflicting hold changed the acknowledgement in force")
	}
	if !fixture.source.Holds(first.Incarnation) {
		t.Error("a refused conflicting hold released the source")
	}
}

// Writer admission and sealing serialize on one durable boundary. Either the
// ticket is in the captured drain set, or issuance is refused as sealed.
func TestWriterAdmissionAndSealingSerializeOnOneBoundary(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)

	// The control, first: a ticket issued on an OPEN source is admitted, and
	// "everything is refused" cannot pass this scenario.
	issued, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("issuing a ticket before the seal: %v", err)
	}
	if err := issued.ValidateAs(output.CaptureWriterTicketIssued); err != nil {
		t.Fatalf("the issue statement is not an issue: %v", err)
	}
	if issued.WriterFence == 0 {
		t.Error("an issued ticket carries no writer fence")
	}

	started, err := fixture.source.BeginSeal(context.Background(), output.SealRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		Incarnation:     hold.Incarnation,
		CaptureFence:    captureFence,
		DeadlineAt:      output.NewTimestamp(fixedNow().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("beginning the seal: %v", err)
	}
	if err := started.Validate(); err != nil {
		t.Fatalf("the seal start does not validate: %v", err)
	}
	// The open ticket is in the captured drain set. A seal that captured an
	// empty set while a writer was admitted is the whole failure this is
	// written against.
	if len(started.DrainSet) != 1 || started.DrainSet[0] != testTicket {
		t.Errorf("the captured drain set is %v", started.DrainSet)
	}

	// After the seal, issuance is refused -- for every kind of writer, which is
	// what Req 12's list means.
	if _, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicketB)); !errors.Is(err, output.ErrSealed) {
		t.Errorf("a ticket was issued after the seal: %v", err)
	}

	// And the seal is not confirmed until the captured set has drained. A
	// confirmation offered with no evidence is refused rather than believed.
	if _, err := fixture.source.ConfirmSeal(context.Background(), output.SealConfirmation{
		Started:      started,
		CaptureFence: captureFence,
		ObservedAt:   output.NewTimestamp(fixedNow()),
	}); !errors.Is(err, output.ErrSealUnconfirmed) {
		t.Errorf("a seal was confirmed with the captured set outstanding: %v", err)
	}

	closed, err := fixture.source.RetireWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("retiring the ticket: %v", err)
	}
	confirmed, err := fixture.source.ConfirmSeal(context.Background(), output.SealConfirmation{
		Started:      started,
		Drained:      []output.DrainedWriter{{WriterTicketID: testTicket, Closed: closed}},
		CaptureFence: captureFence,
		ObservedAt:   output.NewTimestamp(fixedNow()),
	})
	if err != nil {
		t.Fatalf("confirming the seal: %v", err)
	}
	if err := confirmed.ValidateAs(output.CaptureSealConfirmed); err != nil {
		t.Errorf("the confirmation is not a confirmation: %v", err)
	}
	if err := output.VerifyCaptureAcknowledgement(confirmed, fixture.public); err != nil {
		t.Errorf("the confirmation does not verify: %v", err)
	}

	// The seal survives a restart. A daemon that forgot it was sealed would
	// admit a writer to bytes somebody is already canonicalizing.
	fixture.restart(t)
	if _, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicketB)); !errors.Is(err, output.ErrSealed) {
		t.Errorf("a restarted daemon admitted a writer to a sealed source: %v", err)
	}
}

func writerAdmission(hold output.CaptureAcknowledgement, ticket output.WriterTicketID) output.WriterAdmission {
	return output.WriterAdmission{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		Incarnation:     hold.Incarnation,
		WriterTicketID:  ticket,
		WriterFence:     1,
		PodUID:          testPod,
	}
}

// Containment: no control API accepts a path, and the incarnation root is
// resolved through the daemon's own os.Root handle.
func TestNoSourceControlOperationAcceptsAPathAndASwappedSymlinkIsRefused(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)

	// The control: the server-issued incarnation resolves.
	if _, err := fixture.source.ResolveIncarnation(hold.Incarnation); err != nil {
		t.Fatalf("the server-issued incarnation does not resolve: %v", err)
	}

	for name, mutate := range map[string]func(*output.SourceIncarnation){
		"a stale handle generation": func(i *output.SourceIncarnation) { i.HandleGeneration++ },
		"another node":              func(i *output.SourceIncarnation) { i.NodeUID = "node-2" },
		"another execution":         func(i *output.SourceIncarnation) { i.ExecutionID = "44444444-4444-4444-8444-444444444444" },
		"another output":            func(i *output.SourceIncarnation) { i.Output = "other" },
		"a traversing output name":  func(i *output.SourceIncarnation) { i.Output = "../../etc" },
		"an absolute output name":   func(i *output.SourceIncarnation) { i.Output = "/etc/passwd" },
	} {
		foreign := hold.Incarnation
		mutate(&foreign)

		if _, err := fixture.source.ResolveIncarnation(foreign); err == nil {
			t.Errorf("%s resolved to a location on this node", name)
		}
	}

	// A symlink swapped under the source path is refused. This is the case a
	// filepath.Join would walk straight through.
	root, err := fixture.source.ResolveIncarnation(hold.Incarnation)
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("removing: %v", err)
	}
	if err := os.Symlink("/etc", root); err != nil {
		t.Fatalf("swapping in a symlink: %v", err)
	}
	if _, err := fixture.source.ResolveIncarnation(hold.Incarnation); err == nil {
		t.Error("a symlink swapped under the source path was followed")
	} else if !strings.Contains(err.Error(), "containment") {
		t.Errorf("the refusal does not say what it was: %v", err)
	}

	// And the harder half: a RELATIVE symlink to a location inside the tree.
	//
	// os.Root refuses the escape above on its own -- and it refuses any
	// absolute link, whatever it points at -- so neither of those rows
	// distinguishes a resolution that lstats from one that stats. This one
	// does: a relative link inside the root is a link os.Root will happily
	// follow, and a stat here reports a perfectly good directory that is
	// somebody else's bytes about to be sealed as this capture's.
	inside := filepath.Join(fixture.dir, "steps", "somebody-elses")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatalf("creating a sibling: %v", err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatalf("removing the link: %v", err)
	}
	relative, err := filepath.Rel(filepath.Dir(root), inside)
	if err != nil {
		t.Fatalf("computing a relative link: %v", err)
	}
	if err := os.Symlink(relative, root); err != nil {
		t.Fatalf("linking inside the tree: %v", err)
	}
	if resolved, err := fixture.source.ResolveIncarnation(hold.Incarnation); err == nil {
		t.Errorf("a symlink to %s inside the managed tree resolved to %s", inside, resolved)
	} else if !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("the refusal does not name the link: %v", err)
	}
	if fixture.source.Holds(hold.Incarnation) {
		t.Error("the daemon reports it holds a source that is a link to somebody else's")
	}
}

// The fenced release pair, both halves.
//
// "The hold is gone" needs its presence half, and the release is the only thing
// that makes it gone: hold, assert held, release, assert released.
func TestAReleasedHoldIsGoneFromTheNodeAndAHeldOneIsNot(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)

	if !fixture.source.Holds(hold.Incarnation) {
		t.Fatal("the source is not held")
	}

	intent := output.ReleaseIntent{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     output.DispositionNoCapture,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		SourceLeaseID:   testLease,
		ReleaseIntentID: testIntent,
		Incarnation:     hold.Incarnation,
	}
	released, err := fixture.source.AcknowledgeRelease(context.Background(), intent)
	if err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if err := output.VerifyReleaseAcknowledgement(released, fixture.public); err != nil {
		t.Errorf("the release does not verify: %v", err)
	}
	if fixture.source.Holds(hold.Incarnation) {
		t.Error("the source is still held after an acknowledged release")
	}

	// Idempotent for the same intent, and a conflict for another one.
	again, err := fixture.source.AcknowledgeRelease(context.Background(), intent)
	if err != nil {
		t.Fatalf("repeating the release: %v", err)
	}
	if again.Signature != released.Signature {
		t.Error("a repeated release for the same intent produced a second statement")
	}
	other := intent
	other.ReleaseIntentID = "99999999-9999-4999-8999-999999999999"
	if _, err := fixture.source.AcknowledgeRelease(context.Background(), other); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a second release intent was acknowledged: %v", err)
	}

	// The capture branch owes one too, when it terminally cancels before the
	// publish point. This is the branch review's F7, and it is the reason the
	// disposition is a parameter here rather than assumed.
	second := newSourceLedger(t)
	secondHold := held(t, second)
	captureRelease, err := second.source.AcknowledgeRelease(context.Background(), output.ReleaseIntent{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     output.DispositionCapture,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		SourceLeaseID:   testLease,
		ReleaseIntentID: testIntent,
		Incarnation:     secondHold.Incarnation,
	})
	if err != nil {
		t.Fatalf("releasing a cancelled capture's source: %v", err)
	}
	if captureRelease.Disposition != output.DispositionCapture {
		t.Errorf("the release names disposition %s", captureRelease.Disposition)
	}
}

// A stale fence is refused while the current one is served, and a takeover is
// what makes a fence stale.
func TestAStaleFenceIsRefusedWhileTheCurrentOneIsServed(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)

	// The control.
	if _, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicket)); err != nil {
		t.Fatalf("the current fence was refused: %v", err)
	}

	// The takeover: the base ledger advances the execution fence, and the
	// previous owner's next control operation is refused.
	if err := fixture.ledger.Admit(envelope(2)); err != nil {
		t.Fatalf("taking over: %v", err)
	}
	stale := writerAdmission(hold, testTicketB)
	if _, err := fixture.source.AdmitWriter(context.Background(), stale); !errors.Is(err, executioncontrol.ErrStaleFence) {
		t.Errorf("a stale fence was admitted: %v", err)
	}

	current := stale
	current.Execution.Fence = 2
	if _, err := fixture.source.AdmitWriter(context.Background(), current); err != nil {
		t.Errorf("the current fence was refused after the takeover: %v", err)
	}

	// A REPEATED hold at the superseded fence is a read, and reads are served.
	// It returns the stored statement -- the one the first hold returned, at
	// fence 1 -- and grants nothing; a takeover keeps every durable statement,
	// and the previous owner asking what this node said is not the previous
	// owner acting.
	//
	// This is also why the hold replay repairs its cleanup gate through
	// EnsureGateOpen, which takes no fence. A fenced OpenGate here would turn
	// this read into a stale-fence refusal, and the crash it repairs does not
	// care which controller replays the hold.
	replayed, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		hold.Incarnation, testPod)
	if err != nil {
		t.Errorf("a repeated hold at the superseded fence was refused: %v", err)
	} else if !sameCaptureStatement(replayed, hold) {
		t.Error("a repeated hold at the superseded fence returned a different statement")
	}
}

// A registry recreated on this node does not recreate authority.
func TestRestartingRecoversEveryStatementAndRecreatesNoAuthority(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)
	issued, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}

	fixture.restart(t)

	recovered, err := fixture.source.InspectHold(testHandoff, identity(1))
	if err != nil {
		t.Fatalf("inspecting after a restart: %v", err)
	}
	if !sameCaptureStatement(recovered, hold) {
		t.Errorf("a restarted daemon returned a different hold:\nbefore: %+v\n after: %+v",
			hold, recovered)
	}
	if !fixture.source.Holds(hold.Incarnation) {
		t.Error("a restarted daemon does not hold the source it acknowledged holding")
	}

	// The ticket is still outstanding, so a seal after the restart still has to
	// wait for it. A daemon that forgot would confirm a seal over bytes a
	// writer is still holding open.
	started, err := fixture.source.BeginSeal(context.Background(), output.SealRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		Incarnation:     hold.Incarnation,
		CaptureFence:    captureFence,
		DeadlineAt:      output.NewTimestamp(fixedNow().Add(time.Hour)),
	})
	if err != nil {
		t.Fatalf("sealing after a restart: %v", err)
	}
	if len(started.DrainSet) != 1 || started.DrainSet[0] != issued.WriterTicketID {
		t.Errorf("a restarted daemon captured drain set %v", started.DrainSet)
	}

	// And a hold for a handoff this node never acknowledged is not invented by
	// the act of asking about it.
	if _, err := fixture.source.InspectHold("99999999-9999-4999-8999-999999999999", identity(1)); !errors.Is(err, output.ErrNotFound) {
		t.Errorf("a hold nobody established was answered: %v", err)
	}
}

func sameCaptureStatement(left, right output.CaptureAcknowledgement) bool {
	unsigned := func(ack output.CaptureAcknowledgement) string {
		ack.Signature = ""

		return string(output.CanonicalCaptureAcknowledgementBytes(ack))
	}

	return left.Signature == right.Signature && unsigned(left) == unsigned(right)
}

// crashOnPut makes the Nth durable record write fail, and nothing before it.
//
// The stage is the FIRST one in a put, so a crash there leaves the previous
// record whole -- which is the point: what these two tests are about is not a
// torn record but a step that is two records and a crash between them.
func crashOnPut(fixture *sourceFixture, nth int) {
	seen := 0
	fixture.store.fault = func(at faultStage) error {
		if at != faultBeforeTempWrite {
			return nil
		}
		seen++
		if seen == nth {
			return errInjectedCrash
		}

		return nil
	}
}

// A hold is a record AND a cleanup gate, and a crash between them must not
// leave a source that cleanup may destroy.
//
// The reservation opened the gate and wrote a `reserved` record before this
// Pod existed; the hold's own record write is the one that crashes here, so
// what survives the crash is a directory the ATC has already mounted into a
// running Pod with no `held` record naming it. If the gate did not survive
// with it, `CleanupEligible` would say yes over a source the producer is
// writing into, which is the one answer that cannot be taken back: Req 3's
// "the hold prevents cleanup", failing open.
//
// The pair is the assertion after the replay: the gate is there, and the
// source is still on the node.
func TestAHoldReplayedAfterACrashStillGatesCleanup(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	// The reservation comes first, as it does in production: the ATC asks for
	// the location before it builds the Pod, and the crash under test is the
	// one INSIDE the hold.
	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}

	crashOnPut(fixture, 1)
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("the injected crash between the hold record and its gate was not reported: %v", err)
	}
	fixture.store.fault = nil

	// A restart is the only reader that matters: the process that crashed has
	// no memory left.
	fixture.restart(t)

	replayed, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod)
	if err != nil {
		t.Fatalf("replaying the hold after the crash: %v", err)
	}
	if !fixture.source.Holds(replayed.Incarnation) {
		t.Fatal("the replayed hold names an incarnation this node does not hold")
	}

	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		t.Fatalf("finishing: %v", err)
	}
	eligible, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking about cleanup: %v", err)
	}
	if eligible.Eligible {
		t.Errorf("a source held across a crash was cleanup-eligible: gates %v", eligible.OpenExtensionGates)
	}
	if len(eligible.OpenExtensionGates) != 1 || eligible.OpenExtensionGates[0] != SourceHoldGate {
		t.Errorf("the hold's gate did not survive the crash: %v", eligible.OpenExtensionGates)
	}
}

// The repair the test above does not reach, on its own vector.
//
// With the write order gate-then-record, a crash at the second put leaves a
// GATE and no record, so the replay takes the new-hold path and `OpenGate` is
// idempotent -- the replay branch's `EnsureGateOpen` is never entered by that
// test at all. Delete the call and every committed test stays green, which is
// the definition of an unpinned repair.
//
// So this test reaches the state the repair exists for directly: a `held`
// record whose gate is gone. The ledger's own API cannot produce it any more,
// which is the point -- a gate can still be lost to a ledger written by the
// PREVIOUS order, to an operator's edit, or to any half-write nobody has
// thought of yet. `CloseGate` here is not a scenario, it is the damage.
//
// The pair is the before and the after: cleanup is eligible with the gate gone,
// and the replay -- which returns the SAME statement, so it is a read, not a
// new hold -- puts it back.
func TestAHoldReplayRepairsAGateLostBehindTheLedgersBack(t *testing.T) {
	fixture := newSourceLedger(t)
	first := held(t, fixture)

	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		t.Fatalf("finishing: %v", err)
	}

	// The damage: the gate, gone, while the record still says `held`.
	if err := fixture.ledger.CloseGate(identity(1), SourceHoldGate); err != nil {
		t.Fatalf("closing the gate behind the ledger's back: %v", err)
	}

	// The control. Without this line a repair that never ran would look the
	// same as a gate that was never lost.
	before, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking about cleanup: %v", err)
	}
	if !before.Eligible || len(before.OpenExtensionGates) != 0 {
		t.Fatalf("the damage did not take: eligible=%v gates=%v",
			before.Eligible, before.OpenExtensionGates)
	}
	if !fixture.source.Holds(first.Incarnation) {
		t.Fatal("the source went away with the gate; this test is about a held source")
	}

	replayed, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		first.Incarnation, testPod)
	if err != nil {
		t.Fatalf("replaying the hold: %v", err)
	}
	if !sameCaptureStatement(replayed, first) {
		t.Errorf("the replay minted a fresh hold instead of returning the stored one: seq %d, was %d",
			replayed.LedgerSequence, first.LedgerSequence)
	}

	after, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking about cleanup after the replay: %v", err)
	}
	if after.Eligible {
		t.Errorf("the replay did not repair the gate: cleanup is eligible over a held source")
	}
	if len(after.OpenExtensionGates) != 1 || after.OpenExtensionGates[0] != SourceHoldGate {
		t.Errorf("the replay left gates %v; the hold's gate is not back",
			after.OpenExtensionGates)
	}
}

// The mirror, and it fails the other way: closed and stuck.
//
// A release writes the released record, removes the bytes and closes the gate.
// A crash before the close leaves the gate open over a source that is gone, and
// the replay returns the stored statement without closing it -- so the
// execution is never cleanup-eligible again, for a hold nothing holds.
func TestAReleaseReplayedAfterACrashStillClosesTheGate(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)

	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		t.Fatalf("finishing: %v", err)
	}

	intent := output.ReleaseIntent{
		ProtocolVersion: output.ProtocolVersion,
		Disposition:     output.DispositionCapture,
		Execution:       identity(1),
		ActivationEpoch: testEpoch,
		HandoffID:       testHandoff,
		SourceLeaseID:   testLease,
		ReleaseIntentID: testIntent,
		Incarnation:     hold.Incarnation,
	}

	crashOnPut(fixture, 2)
	if _, err := fixture.source.AcknowledgeRelease(context.Background(), intent); !errors.Is(err, errInjectedCrash) {
		t.Fatalf("the injected crash between the release record and the gate close was not "+
			"reported: %v", err)
	}
	fixture.store.fault = nil
	fixture.restart(t)

	replayed, err := fixture.source.AcknowledgeRelease(context.Background(), intent)
	if err != nil {
		t.Fatalf("replaying the release after the crash: %v", err)
	}
	if replayed.ReleaseIntentID != testIntent {
		t.Errorf("the replayed release names intent %s", replayed.ReleaseIntentID)
	}
	if fixture.source.Holds(hold.Incarnation) {
		t.Error("the released source is still on the node after the replay")
	}

	eligible, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking about cleanup: %v", err)
	}
	if !eligible.Eligible {
		t.Errorf("a released source's gate was never closed: %s (gates %v)",
			eligible.WithheldReason, eligible.OpenExtensionGates)
	}
}

// A ticket's replay is the ticket's own statement, and the ticket is bound to
// the process that was issued it.
//
// The record stored ticket IDs and nothing else, so three things followed. A
// re-presented ticket replayed the HOLD statement -- kind hold_acknowledged,
// no writer ticket id, an older sequence -- rather than the issue statement the
// first call returned. The same id from another Pod UID, or at another writer
// fence, was admitted as if it were the same writer: Req 13's "a ticket cannot
// be transferred to a new process, Pod UID, handle generation, or fence epoch",
// with three of the four unchecked. And a retire replay minted a FRESH
// signature each time, so two answers to one close disagreed about their
// sequence.
func TestAWriterTicketReplaysItsOwnStatementAndIsBoundToItsProcess(t *testing.T) {
	fixture := newSourceLedger(t)
	hold := held(t, fixture)

	issued, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("issuing: %v", err)
	}

	replayed, err := fixture.source.AdmitWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("replaying the ticket: %v", err)
	}
	if !sameCaptureStatement(replayed, issued) {
		t.Errorf("the ticket replay returned a different statement:\n first: kind=%s ticket=%q seq=%d\n"+
			"replay: kind=%s ticket=%q seq=%d", issued.Kind, issued.WriterTicketID,
			issued.LedgerSequence, replayed.Kind, replayed.WriterTicketID, replayed.LedgerSequence)
	}

	// The same ticket, from another pod or at another writer fence, is a
	// different writer wearing the same name.
	for name, mutate := range map[string]func(*output.WriterAdmission){
		"another pod":          func(a *output.WriterAdmission) { a.PodUID = "pod-2" },
		"another writer fence": func(a *output.WriterAdmission) { a.WriterFence = 9 },
	} {
		moved := writerAdmission(hold, testTicket)
		mutate(&moved)
		if _, err := fixture.source.AdmitWriter(context.Background(), moved); !errors.Is(err, output.ErrConflict) {
			t.Errorf("a ticket presented from %s was not a typed conflict: %v", name, err)
		}
	}

	// And a ticket that names NO pod is refused before it is issued, rather
	// than admitted and then replayed for the next caller that also left the
	// field out. `sameWriter` compares two empty strings and calls them one
	// process, so an unbound ticket is a ticket bound to nothing at all -- the
	// binding the two rows above assert, made vacuous by omission.
	unbound := writerAdmission(hold, testTicketB)
	unbound.PodUID = ""
	if _, err := fixture.source.AdmitWriter(context.Background(), unbound); !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("a writer admission naming no pod was not refused as incomplete: %v", err)
	}

	closed, err := fixture.source.RetireWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("retiring: %v", err)
	}
	if err := closed.ValidateAs(output.CaptureWriterTicketClosed); err != nil {
		t.Fatalf("the close statement is not a close: %v", err)
	}
	again, err := fixture.source.RetireWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("retiring again: %v", err)
	}
	if !sameCaptureStatement(again, closed) {
		t.Errorf("a retire replay minted a fresh statement: seq %d then %d",
			closed.LedgerSequence, again.LedgerSequence)
	}

	// And every one of those statements survives a restart, because it is what
	// the record holds rather than what a signer would produce again.
	fixture.restart(t)
	afterRestart, err := fixture.source.RetireWriter(context.Background(), writerAdmission(hold, testTicket))
	if err != nil {
		t.Fatalf("retiring after a restart: %v", err)
	}
	if !sameCaptureStatement(afterRestart, closed) {
		t.Errorf("the close statement did not survive a restart: seq %d, was %d",
			afterRestart.LedgerSequence, closed.LedgerSequence)
	}
}

// The incarnation is reserved BEFORE the Pod exists, and the hold binds to it.
//
// This is the Phase 4 completion pass's whole point. Before it, the daemon
// minted the incarnation inside AcknowledgeHold -- which cannot happen before
// the Pod exists, because the hold is bound to the admitted Pod UID -- so the
// ATC had nothing to mount and the producer wrote into a sibling directory no
// hold protected. Reserving first is what lets the ATC repeat a location the
// daemon chose, and Req 7 still holds because the ATC composes nothing.
func TestTheIncarnationIsReservedBeforeThePodExistsAndTheHoldBindsToIt(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving the incarnation: %v", err)
	}
	if err := reserved.Validate(); err != nil {
		t.Fatalf("the reservation does not validate: %v", err)
	}
	if reserved.Incarnation.HandleGeneration == 0 {
		t.Error("the daemon reserved no handle generation")
	}
	if reserved.Incarnation.NodeUID != testNode {
		t.Errorf("the reservation names node %s", reserved.Incarnation.NodeUID)
	}
	if reserved.Directory != reserved.Incarnation.Directory() {
		t.Errorf("the reservation's directory %q is not the incarnation's %q",
			reserved.Directory, reserved.Incarnation.Directory())
	}

	// The directory is REALLY there, under the daemon's own root, before any
	// Pod exists. A reservation the ATC could mount and the kubelet could not
	// find would be a hostPath the node creates with the wrong ownership.
	onDisk := filepath.Join(fixture.dir, "steps", reserved.Directory)
	info, statErr := os.Lstat(onDisk)
	if statErr != nil {
		t.Fatalf("the reserved incarnation is not on the node: %v", statErr)
	}
	if !info.IsDir() {
		t.Errorf("the reserved incarnation is a %s, not a directory", info.Mode().Type())
	}

	// It is owned by the LEDGER from this moment, which is what makes every
	// path-keyed guard load-bearing while the producer is still writing. The
	// read-only classifier is the one every destructive path on this node asks.
	classifier := ledger.New(fixture.dir)
	if class := classifier.Classify(reserved.Directory); class.Destructive() {
		t.Errorf("a reserved incarnation classified as %s; destructive cleanup would be "+
			"permitted over the directory the producer is about to write into", class)
	}
	// And the ancestor question, which is the one the sweeper asks: the
	// reservation is a top-level entry under steps/, and a sweep that removed
	// it would take the source with it.
	parent := reserved.Directory[:strings.Index(reserved.Directory, "/")]
	if class := classifier.Classify(parent); class.Destructive() {
		t.Errorf("the reservation's parent directory %q classified as %s", parent, class)
	}

	// Idempotent for the same identity and fence: the same location, not a
	// second one. A reservation that minted a fresh generation per call would
	// leave the first directory orphaned and the hold pointing at the wrong
	// bytes after any retry.
	again, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("repeating the reservation: %v", err)
	}
	if again.Incarnation != reserved.Incarnation {
		t.Errorf("a repeated reservation issued %v and the first issued %v",
			again.Incarnation, reserved.Incarnation)
	}

	// A DIFFERENT fence is a typed conflict, and the first reservation stands.
	differentFence := admission()
	differentFence.Execution.Fence++
	if _, err := fixture.source.ReserveIncarnation(context.Background(), differentFence); err == nil {
		t.Error("a reservation at a different fence was admitted")
	} else if !errors.Is(err, output.ErrConflict) && !errors.Is(err, executioncontrol.ErrStaleFence) {
		t.Errorf("a reservation at a different fence was refused untyped: %v", err)
	}
	standing, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("re-reading the standing reservation: %v", err)
	}
	if standing.Incarnation != reserved.Incarnation {
		t.Error("the refused reservation replaced the standing one")
	}

	// The hold BINDS to it. A hold offering a different incarnation is refused
	// however well formed it is, because the ATC has already mounted the
	// reserved one into the producing Pod and a hold over anything else would
	// protect bytes nobody is writing.
	foreign := reserved.Incarnation
	foreign.HandleGeneration += 100
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(), foreign, testPod); err == nil {
		t.Error("a hold naming an incarnation the daemon did not reserve was acknowledged")
	} else if !errors.Is(err, output.ErrConflict) {
		t.Errorf("a hold over an unreserved incarnation was refused untyped: %v", err)
	}

	ack, err := fixture.source.AcknowledgeHold(context.Background(), admission(), reserved.Incarnation, testPod)
	if err != nil {
		t.Fatalf("holding the reserved source: %v", err)
	}
	if ack.Incarnation != reserved.Incarnation {
		t.Errorf("the hold acknowledged %v and the reservation issued %v",
			ack.Incarnation, reserved.Incarnation)
	}
}

// An unreserved execution cannot be held at all.
//
// The control is the line below it: the same admission, after a reservation,
// is acknowledged. Without that line this would pass on a daemon that refused
// every hold.
func TestAHoldOverAnUnreservedExecutionIsRefusedAndAReservedOneIsNot(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	_, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		output.SourceIncarnation{}, testPod)
	if err == nil {
		t.Fatal("a hold with no reservation behind it was acknowledged; the incarnation would " +
			"have been minted after the Pod was built, which is the seam this pass closes")
	}
	if !errors.Is(err, output.ErrNotFound) {
		t.Errorf("an unreserved hold was refused untyped: %v", err)
	}

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod); err != nil {
		t.Fatalf("the same hold was refused after a reservation: %v", err)
	}
}

// The reservation is DURABLE: it survives the daemon, and the replay proves it.
//
// A reservation kept only in memory would be a hostPath the ATC mounted into a
// Pod that outlives the daemon process, with nothing on this node saying the
// directory is spoken for -- so the next sweep would reclaim it while the
// producer was still writing.
func TestAReservationSurvivesARestartAndTheReplayReturnsTheSameLocation(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}

	fixture.restart(t)

	replayed, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("replaying the reservation after a restart: %v", err)
	}
	if replayed.Incarnation != reserved.Incarnation {
		t.Errorf("the restarted daemon reserved %v and the reservation before the restart was %v",
			replayed.Incarnation, reserved.Incarnation)
	}
	if replayed.Directory != reserved.Directory {
		t.Errorf("the restarted daemon named directory %q and the reservation named %q",
			replayed.Directory, reserved.Directory)
	}

	// And a fresh reservation for a DIFFERENT handoff still gets its own
	// location, so the replay is a replay rather than a ledger that has stopped
	// issuing.
	other := admission()
	other.HandoffID = output.HandoffID("33333333-3333-4333-8333-333333333333")
	other.SourceLeaseID = output.SourceLeaseID("44444444-4444-4444-8444-444444444444")
	fresh, err := fixture.source.ReserveIncarnation(context.Background(), other)
	if err != nil {
		t.Fatalf("reserving for a second handoff: %v", err)
	}
	if fresh.Incarnation.HandleGeneration == reserved.Incarnation.HandleGeneration {
		t.Error("a second handoff was given the first's handle generation")
	}
}

// The Pod UID is bound ONCE, at the hold, from inside the Pod.
//
// This is the finding the Phase 4 round-1 review turned up by driving the
// sequence a real producer has: admission and reservation both precede the Pod,
// so neither can name one, and the hold is the first message that can. Before
// this the hold read the Pod UID off the base admission -- which for a caller
// that reserves before building a Pod is the empty string, so the producer's
// own start was then refused for not being the Pod the hold named.
//
// Three arms and a control, and the control is first: with a Pod UID presented,
// the hold names it and the same UID replays to the same statement.
func TestTheHoldBindsThePodUIDOnceAndRefusesADifferentOneAtTheSameFence(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}

	// The control. The reservation named no Pod and the hold binds one.
	first, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod)
	if err != nil {
		t.Fatalf("holding: %v", err)
	}
	if first.PodUID != testPod {
		t.Fatalf("the hold bound pod %q and the init container presented %q",
			first.PodUID, testPod)
	}

	// The same Pod replays to the same statement.
	again, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod)
	if err != nil {
		t.Fatalf("repeating the hold from the same pod: %v", err)
	}
	if !sameCaptureStatement(again, first) {
		t.Error("a repeat from the same pod returned a different statement")
	}

	// A DIFFERENT Pod at the same fence is a typed conflict. No takeover, no
	// epoch bump, nothing Phase 5 owns: a replaced Pod is simply a new
	// incarnation, and it does not inherit a hold over bytes the previous one
	// was writing.
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, "pod-2"); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a second hold from another pod at the same fence was not a typed conflict: %v", err)
	}

	// And the first hold is still the one in force.
	current, err := fixture.source.InspectHold(testHandoff, identity(1))
	if err != nil {
		t.Fatalf("inspecting: %v", err)
	}
	if current.PodUID != testPod {
		t.Errorf("a refused second hold rebound the pod to %q", current.PodUID)
	}

	// A writer admission from a pod the hold does not name is refused too --
	// with a ticket id nothing has issued, so this is the NEW-ticket arm rather
	// than the ticket-transfer one above it.
	foreignWriter := writerAdmission(first, testTicketB)
	foreignWriter.PodUID = "pod-2"
	if _, err := fixture.source.AdmitWriter(context.Background(), foreignWriter); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a writer ticket for a pod the hold does not name was issued: %v", err)
	}
}

// A hold with no Pod at all is refused rather than recorded empty.
//
// An empty UID compares equal to the next empty one, so a hold that bound
// nothing would then admit every later Pod as "the same" -- which is precisely
// the vacuity the bind-once rule exists to prevent.
func TestAHoldThatNamesNoPodIsRefused(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}

	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, ""); !errors.Is(err, output.ErrIncomplete) {
		t.Errorf("a hold naming no pod was not refused as incomplete: %v", err)
	}

	// The control: the same hold, with the Downward API's value, is acknowledged.
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod); err != nil {
		t.Fatalf("the same hold with a pod was refused: %v", err)
	}
}

// A hold taken on a node other than the reserving one is refused, by name.
//
// The reservation is a directory on ONE node's disk. A scheduler that placed
// the producer elsewhere would have its control init dial its own node's
// daemon, which reserved nothing -- and the hostPath's DirectoryOrCreate would
// have made an empty unheld directory under it. This is the typed refusal; the
// Pod's required affinity on the reserving node is the other half.
func TestAHoldFromANodeOtherThanTheReservingOneIsRefused(t *testing.T) {
	fixture := newSourceLedger(t)
	admitted(t, &fixture.ledgerFixture)

	reserved, err := fixture.source.ReserveIncarnation(context.Background(), admission())
	if err != nil {
		t.Fatalf("reserving: %v", err)
	}

	elsewhere := reserved.Incarnation
	elsewhere.NodeUID = "node-somewhere-else"
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		elsewhere, testPod); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a hold naming an incarnation reserved on another node was not refused: %v", err)
	}

	// The control, and it is what keeps the arm from being "every hold is
	// refused": the same hold on the reserving node is acknowledged.
	if _, err := fixture.source.AcknowledgeHold(context.Background(), admission(),
		reserved.Incarnation, testPod); err != nil {
		t.Fatalf("the hold on the reserving node was refused: %v", err)
	}
}
