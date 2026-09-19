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
	Lease     output.SourceHoldID
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
		Lease:     output.SourceHoldID(uuid.NewString()),
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
		SourceHoldID:    admitted.Lease,
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
		SourceHoldID:    c.Lease,
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
	// The source release, which is the last thing a capture owes. Req 11 orders
	// "an exact receipt ... before a fenced source release", and that ordering
	// presupposes the release: a held source is exempt from payload cleanup,
	// sweep and reuse (Reqs 3 and 9), so a successful capture that never
	// released would pin its incarnation on the node forever.
	//
	// The release is the HOLD's. The bytes stay -- they are the step's output,
	// aliased read-only at the ordinary path -- and only reclamation by policy
	// removes them.
	if !record.ReleaseAcknowledged {
		t.Error("a registered capture never released its source, so the incarnation is held on " +
			"the node forever and the execution is never cleanup-eligible")
	}
	if !c.sourceStillThere() {
		t.Error("the release deleted the captured output from the node")
	}
	if record.Settled != true {
		t.Error("a capture with a registered receipt and an acknowledged release is not settled")
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
	if len(h.announcementKinds(t, c.Handoff)) == 0 {
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

// A Kubernetes timeout at the drain boundary, carried past the seal deadline:
// no complete final container status before the deadline.
//
// The DEADLINE is what makes it terminal, and it is the reason this spec moves
// the row's seal deadline into the past rather than relying on the drain
// refusing forever. Requirement 17 types `seal_unconfirmed` as the
// outcome of a boundary that could not be proved *before a database-clock
// deadline*; a boundary that cannot be proved right now is retried, which is
// the spec below this one. What it DOES owe is a fenced release -- the source
// is still on a node -- and the handoff is not settled until that release is
// acknowledged.
func TestAnUnprovableDrainIsSealUnconfirmedAndPublishesNothing(t *testing.T) {
	h := newHarness(t)
	h.Drain.Unprovable = true

	c := h.admit(t).hold(t).finish(t, true)
	advanceExactly(t, c, 1)
	stampAnExpiredSealDeadline(t, h, c, time.Minute)
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
	// And the release released the HOLD. The bytes are the step's own output,
	// aliased read-only at the ordinary path, and a terminal capture failure is
	// not a reason to delete a step's output from under a build.
	if !c.sourceStillThere() {
		t.Error("the release deleted the step's output; a release closes the hold and leaves " +
			"the bytes to the artifact daemon's ordinary lifecycle")
	}
}

// The SEAL DEADLINE, enforced: a seal whose boundary is not proved before its
// database-clock deadline is `seal_unconfirmed` and publishes nothing.
//
// The deadline is composed at begin_seal and it used to be read by nothing --
// the daemon stored neither it nor a seal-begun time, and the coordinator held
// no durable moment to compare `now()` against -- so a seal an hour past its
// deadline completed and registered a receipt. The only producer of
// `seal_unconfirmed` was the mistaken one a lost answer made.
//
// The drain here PROVES: the refusal is the deadline's alone, which is what
// makes this a spec about the clock rather than a second copy of the one above.
func TestASealPastItsDeadlineIsSealUnconfirmed(t *testing.T) {
	h := newHarness(t)

	c := h.admit(t).hold(t).finish(t, true)
	advanceExactly(t, c, 1)
	stampAnExpiredSealDeadline(t, h, c, time.Hour)
	c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateFailed {
		t.Fatalf("a seal an hour past its deadline is %s", record.State)
	}
	if record.TerminalFailure != "seal_unconfirmed" {
		t.Errorf("the terminal failure is %q", record.TerminalFailure)
	}
	if record.Receipt != nil {
		t.Error("a seal past its deadline produced a receipt")
	}
	if h.bucketKeys(t) != nil {
		t.Error("a seal past its deadline created an object")
	}
	if record.LogicalResolved {
		t.Error("a seal past its deadline resolved a logical identity")
	}
}

// The database clock decides ADMISSIBILITY, not only terminality.
//
// Requirement 17 names a database-clock deadline, and a deadline that only
// decides whether an already-refused boundary is permanent leaves the question
// it exists to answer -- may a proof that arrives LATE be admitted? -- to
// whichever wall clock happened to be asked. The daemon's own comparison says
// so in its comment: it is defence in depth, and "the deciding clock is the
// database's".
//
// Only the ROW's deadline moves here, by SQL, and the node's copy stays
// healthy: the daemon would confirm this seal, so the only thing that can
// refuse it is the database clock. At the round-2 head it did not, and a seal
// an hour past its deadline registered a receipt whenever the two clocks
// disagreed -- which is not a hypothetical, it is what a node running an hour
// behind IS.
//
// The drain is not asked. That is the point rather than an optimisation:
// proving the boundary terminates the producing Pod, its sidecars and any live
// hijack session, and a capture that is already inadmissible must not do that
// to find out.
func TestASealProvedPastTheDatabaseDeadlineIsNotAdmitted(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal, both under a healthy deadline: the row and the
	// node hold the same one.
	advanceExactly(t, c, 2)

	if _, err := h.Conn.Exec(`
		UPDATE hangar_capture_reservations SET seal_deadline_at = now() - interval '1 hour'
		WHERE reservation_id = $1`, string(c.record(t).ReservationID)); err != nil {
		t.Fatalf("moving the row's seal deadline into the past: %v", err)
	}

	taken := c.advance(t)

	record := c.record(t)
	if record.State != output.CaptureStateFailed {
		t.Fatalf("a seal an hour past the DATABASE deadline is %s after %v, with the node's "+
			"copy of the deadline still healthy", record.State, taken)
	}
	if record.TerminalFailure != "seal_unconfirmed" {
		t.Errorf("the terminal failure is %q", record.TerminalFailure)
	}
	if record.Receipt != nil {
		t.Error("a proof admitted past the database deadline produced a receipt")
	}
	if keys := h.bucketKeys(t); keys != nil {
		t.Errorf("a proof admitted past the database deadline created %v", keys)
	}
	if record.LogicalResolved {
		t.Error("a seal past the database deadline resolved a logical identity")
	}
	if h.Drain.Calls() != 0 {
		t.Errorf("the writer drain was driven %d time(s) for a capture the database clock had "+
			"already made inadmissible; proving a boundary terminates the producing Pod",
			h.Drain.Calls())
	}
	if !record.ReleaseAcknowledged {
		t.Error("the source was never released after the terminal failure")
	}
	if !c.sourceStillThere() {
		t.Error("the release deleted the step's output")
	}
}

// The window a five-minute deadline actually elapses in: DURING the drain.
//
// The spec above reads the database clock before the drain is driven, which is
// the cheap half -- it covers a capture that was already inadmissible when the
// boundary was first considered. The drain is the slow half. It terminates the
// producing Pod and waits for a complete final container status, so "the
// deadline elapsed while the boundary was being proved" is the ORDINARY way a
// seal deadline elapses, and a control plane that reads the clock only before
// the wait has not read it at the moment it decides.
//
// Here the row's deadline moves into the past WHILE the drain runs and the
// node's copy stays healthy, so the daemon would confirm this seal: the
// database clock is the only thing that can refuse it, which is what
// requirement 17 says decides. The `confirm-seal` count is the assertion that
// makes this spec more than a second copy of the one above -- the node is never
// ASKED, so no confirmation is recorded anywhere, and the later passes that
// read a recorded confirmation back as a fact have nothing to read.
func TestAProofCompletedPastTheDatabaseDeadlineIsNotAdmitted(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal, both under a healthy deadline: the row and the
	// node hold the same one.
	advanceExactly(t, c, 2)

	reservation := string(c.record(t).ReservationID)
	var moved error
	h.Drain.WhileDraining = func() {
		_, moved = h.Conn.Exec(`
			UPDATE hangar_capture_reservations SET seal_deadline_at = now() - interval '1 hour'
			WHERE reservation_id = $1`, reservation)
	}

	taken := c.advance(t)
	if moved != nil {
		t.Fatalf("moving the row's seal deadline into the past during the drain: %v", moved)
	}

	if h.Drain.Calls() != 1 {
		t.Fatalf("the drain was driven %d time(s); this spec is about a deadline that elapses "+
			"while the boundary is being proved, so it has to be proved", h.Drain.Calls())
	}

	record := c.record(t)
	if record.State != output.CaptureStateFailed {
		t.Fatalf("a boundary proved past the DATABASE deadline is %s after %v, with the node's "+
			"copy of the deadline still healthy", record.State, taken)
	}
	if record.TerminalFailure != "seal_unconfirmed" {
		t.Errorf("the terminal failure is %q", record.TerminalFailure)
	}
	if asked := h.Dialer.Calls("confirm-seal"); asked != 0 {
		t.Errorf("the node was asked to confirm %d time(s) a seal the database clock had already "+
			"closed; a confirmation recorded at the node is read back as a fact by every later "+
			"pass, so asking for one past the deadline puts the wrong answer beyond the "+
			"deadline's reach", asked)
	}
	if record.Receipt != nil {
		t.Error("a proof completed past the database deadline produced a receipt")
	}
	if keys := h.bucketKeys(t); keys != nil {
		t.Errorf("a proof completed past the database deadline created %v", keys)
	}
	if record.LogicalResolved {
		t.Error("a proof completed past the database deadline resolved a logical identity")
	}
	if !record.ReleaseAcknowledged {
		t.Error("the source was never released after the terminal failure")
	}
	if !c.sourceStillThere() {
		t.Error("the release deleted the step's output")
	}
}

// A deadline that expires WHILE WE WAIT, rather than one that was already past
// when the seal began.
//
// The other deadline specs in this file arrange the past by subtraction, which
// is a fine way to say "already expired" and a poor way to say "expired while
// we waited". Here the row's deadline is ahead when the drain starts, the drain
// takes longer than it, and the row has passed by the time the drain returns.
//
// It is the spec that says WHICH clock carries the refusal. The node keeps the
// deadline it was handed at begin_seal -- minutes ahead, and untouched here --
// so the database's row is the ONLY clock that has expired, and a green says
// its arm fired and fired before the node was asked. Round 3 found the reverse:
// the daemon's wall clock was the only thing refusing this window, and it is
// the clock requirement 17 pointedly does not name. The node's own arm is
// pinned separately, in the spec below.
func TestAProofOverrunningItsDeadlineOnBothClocksIsNotAdmitted(t *testing.T) {
	h := newHarness(t)

	c := h.admit(t).hold(t).finish(t, true)
	advanceExactly(t, c, 2)

	// Three seconds ahead, set after begin_seal: a real forward-going term the
	// drain then overruns. It cannot come from Coordinator.SealDeadline, whose
	// floor is thirty seconds and is a frozen decision rather than a knob a
	// spec may turn.
	if _, err := h.Conn.Exec(`
		UPDATE hangar_capture_reservations SET seal_deadline_at = now() + interval '3 seconds'
		WHERE reservation_id = $1`, string(c.record(t).ReservationID)); err != nil {
		t.Fatalf("giving the row a short forward-going deadline: %v", err)
	}

	h.Drain.WhileDraining = func() { time.Sleep(4 * time.Second) }

	taken := c.advance(t)

	if h.Drain.Calls() != 1 {
		t.Fatalf("the drain was driven %d time(s); the deadline was ahead when the seal began, "+
			"so the boundary has to be proved before it can be proved late", h.Drain.Calls())
	}

	record := c.record(t)
	if record.State != output.CaptureStateFailed {
		t.Fatalf("a boundary that took longer than its own deadline to prove is %s after %v",
			record.State, taken)
	}
	if record.TerminalFailure != "seal_unconfirmed" {
		t.Errorf("the terminal failure is %q", record.TerminalFailure)
	}
	if asked := h.Dialer.Calls("confirm-seal"); asked != 0 {
		t.Errorf("the node was asked to confirm %d time(s); the node's own copy of the deadline "+
			"is still minutes ahead, so the DATABASE's is the only clock that has expired and "+
			"it has to decide before the node is asked -- otherwise the daemon's wall clock is "+
			"carrying requirement 17", asked)
	}
	if record.Receipt != nil {
		t.Error("a boundary proved past its own deadline produced a receipt")
	}
	if keys := h.bucketKeys(t); keys != nil {
		t.Errorf("a boundary proved past its own deadline created %v", keys)
	}
	if !c.sourceStillThere() {
		t.Error("the release deleted the step's output")
	}
}

// And the node's own comparison, which is what is left for it to do.
//
// The daemon refuses to confirm a seal past the deadline it was GIVEN, and its
// comment calls that defence in depth -- the residual window between the
// control plane's last reading of the database clock and the node's record of
// the confirmation. Defence in depth with nothing pinning it is a line of code
// nobody notices removing, and once the database clock refuses before the node
// is asked, no other spec in this file can reach the node's arm at all.
//
// So the two clocks are made to DISAGREE in the direction only the node can
// answer: the node holds a deadline a minute in the past and the database's row
// says an hour ahead. The node refuses; the control plane weighs that refusal
// against its own clock, finds the deadline still ahead, and commits NOTHING --
// which is the second half, and the reason this refusal is typed as an
// unconfirmed seal rather than as a conflict. A drifting node may not expire a
// deadline it is subject to, only decline to answer for it.
func TestTheNodeRefusesToConfirmPastTheDeadlineItWasGiven(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)
	advanceExactly(t, c, 1)
	stampAnExpiredSealDeadline(t, h, c, time.Minute)
	advanceExactly(t, c, 1)

	// The DATABASE extends -- a longer term, an operator's edit, a row restored
	// from a backup. The node keeps the deadline it was handed at begin_seal.
	if _, err := h.Conn.Exec(`
		UPDATE hangar_capture_reservations SET seal_deadline_at = now() + interval '1 hour'
		WHERE reservation_id = $1`, string(c.record(t).ReservationID)); err != nil {
		t.Fatalf("moving the row's seal deadline into the future: %v", err)
	}

	decision, err := c.advanceOnce(t)
	if !errors.Is(err, output.ErrSealUnconfirmed) {
		t.Fatalf("the node confirmed a seal past the deadline it was given (%s): %v",
			decision.Transition, err)
	}
	if asked := h.Dialer.Calls("confirm-seal"); asked != 1 {
		t.Errorf("the node was asked to confirm %d time(s); the database's deadline is ahead, "+
			"so this refusal can only be the node's own", asked)
	}

	record := c.record(t)
	if record.State == output.CaptureStateFailed {
		t.Fatalf("a node's clock committed the capture as terminal %q while the database's "+
			"deadline was an hour ahead; a drifting node may not expire a deadline it is "+
			"subject to", record.TerminalFailure)
	}
	if record.Receipt != nil {
		t.Error("a seal the node refused to confirm produced a receipt")
	}
	if keys := h.bucketKeys(t); keys != nil {
		t.Errorf("a seal the node refused to confirm created %v", keys)
	}
	if !c.sourceStillThere() {
		t.Error("a refusal the database has not agreed with released the step's output")
	}
}

// The deadline's control, and it is the half that says the clock is a clock
// rather than a switch: the SAME typed evidence, before the deadline, is
// retried and committed as nothing.
//
// Without it, "a seal past its deadline fails" passes on a coordinator that
// fails every seal it cannot prove on the first pass -- which is exactly what
// requirement 17 does not say.
func TestAnUnprovableDrainBeforeItsDeadlineIsRetriedNotCommitted(t *testing.T) {
	h := newHarness(t)
	h.Drain.UnprovableOnce = true

	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2 and the seal, then the confirmation that cannot be proved yet.
	for i := 0; i < 2; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}
	if _, err := c.advanceOnce(t); !errors.Is(err, output.ErrSealUnconfirmed) {
		t.Fatalf("an unproved boundary before the deadline was not reported: %v", err)
	}

	interim := c.record(t)
	if interim.State == output.CaptureStateFailed {
		t.Fatalf("a boundary unproved on the first pass was committed as terminal %q with the "+
			"deadline still ahead", interim.TerminalFailure)
	}
	if !c.sourceStillThere() {
		t.Error("the source was released before the seal deadline")
	}

	c.advance(t)
	if final := c.record(t); final.State != output.CaptureStateRegistered {
		t.Errorf("the capture is %s/%q after the boundary was proved",
			final.State, final.TerminalFailure)
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
	// Req 2: a failed producer follows existing task semantics, and existing
	// semantics keep a failed task's outputs on the node for the build's
	// lifetime -- on_failure, hijack and artifact passing to a later step all
	// read them. The release released the hold; it did not delete the step's
	// output out from under the build that is about to look at it.
	if !c.sourceStillThere() {
		t.Error("no_capture deleted the failed producer's output from the node")
	}
}

// A LOST REGISTRATION ANSWER, and what the build is then told.
//
// The receipt registers, the commit's answer is lost, and the next pass reads
// durable state and settles. The capture is right; the DIAGNOSTICS are what
// break, because every announcement was emitted AFTER its commit, outside
// anything recovery re-takes. So the one announcement a build's diagnostics
// exist to show -- the terminal disposition, the thing that explains why hijack
// went away and how the capture ended -- was the announcement most likely to be
// lost, since it is the last one and the one nothing repeats.
//
// It is driven by PRODUCTION's recovery component, and that is the assertion
// rather than a detail. `Recoverer.Run` advances every INCOMPLETE handoff by at
// most one transition, drawn from `IncompleteHandoffs`; a handoff that has
// settled is not incomplete and is never visited again. So a replay hung off
// the `none` transition -- the state a settled handoff is in -- is a replay
// nothing in production ever reaches. A spec that looped `Advance` to
// quiescence took a transition the deployment does not take, and passed.
//
// Req 18. The store is idempotent by (handoff, kind), so a disposition written
// inside its own transaction costs a repeat nothing.
func TestALostRegistrationAnswerStillAnnouncesTheDisposition(t *testing.T) {
	h := newHarness(t)
	ambiguous := &ambiguousTransactor{inner: h.Coordinator.Transactor}
	h.Coordinator.Transactor = ambiguous

	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2, seal, confirm, resolve, publish.
	for i := 0; i < 5; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}

	// register_receipt commits three times: the lease, the stat challenge, and
	// the receipt admission. It is the third one whose answer is lost.
	ambiguous.Skip, ambiguous.LoseNext = 2, true
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost registration answer was not reported: %v", err)
	}

	h.recoverUntilSettled(t, c.Handoff)

	record := c.record(t)
	if record.State != output.CaptureStateRegistered {
		t.Fatalf("the capture is %s after a lost registration answer", record.State)
	}
	if record.Receipt == nil {
		t.Fatal("no receipt was registered")
	}
	if !record.Settled {
		t.Fatal("the recovery component left the handoff unsettled")
	}

	kinds := h.announcementKinds(t, c.Handoff)
	for _, owed := range hangaroutput.AnnouncementKinds() {
		found := false
		for _, said := range kinds {
			if said == owed {
				found = true
			}
		}
		if !found {
			t.Errorf("the capture announced %v and never %q; the announcement a build's "+
				"diagnostics exist to show is the one a lost answer drops", kinds, owed)
		}
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
// inherited, from EVERY half a capture can be interrupted at.
//
// A takeover is what an ATC restart mid-capture becomes, because OwnerID is
// minted per process: after DefaultLeaseTerm the next process is a different
// owner and its first `own` advances the fence. The spec above proves the OLD
// owner is refused; this one proves the NEW one is not, which is the half
// recovery depends on and the half that was broken -- two writes checked the
// reservation ROW's fence, frozen at 1 by Stage 2, while everything else
// checked the lease, so every capture that survived a restart past Stage 2
// ended as an unregistered object nobody could either register or fail.
//
// FOUR halves and not one, because "the lease is the one fence source" is a
// claim about every reader and a takeover after the SEAL is the single half
// where it cannot fail: the new owner makes the logical resolution itself, so
// the resolution's fence and the lease's agree and a reader of the wrong one
// looks correct. The three halves past the resolution are where a second fence
// source shows -- and the slow one, the upload, is the half an ATC restart is
// most likely to land in. Each of them ends `registered` under the new owner or
// it ends as an object in the bucket that nobody can register and nobody can
// fail.
func TestATakeoverCarriesTheCaptureThroughToRegistration(t *testing.T) {
	// The point the FIRST owner is interrupted at. Each returns having driven
	// the capture there and no further.
	halves := []struct {
		name    string
		arrange func(t *testing.T, h *harness, c *capture)
	}{
		{
			// Stage 2 and the seal, under the first owner. A takeover of a
			// lease nobody holds is not a takeover.
			name: "after the seal",
			arrange: func(t *testing.T, _ *harness, c *capture) {
				advanceExactly(t, c, 2)
			},
		},
		{
			// The logical resolution is committed at fence 1 and is immutable.
			// Everything after it is the new owner's, at fence 2.
			name: "after the logical resolution",
			arrange: func(t *testing.T, _ *harness, c *capture) {
				advanceExactly(t, c, 4)
			},
		},
		{
			// The object IS in the bucket and the first owner never learned
			// it. This is F1's own outcome: a marked object, no receipt, and a
			// handoff `IncompleteHandoffs` lists on every pass.
			name: "after a lost publish answer",
			arrange: func(t *testing.T, h *harness, c *capture) {
				advanceExactly(t, c, 4)
				h.Dialer.LoseAfter = "publish"
				if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
					t.Fatalf("the lost publish answer was not reported: %v", err)
				}
			},
		},
		{
			// register_receipt commits three times: the lease, the stat
			// challenge, and the receipt admission. Losing the SECOND leaves a
			// consumed-nothing challenge behind and the capture unregistered.
			name: "after a lost stat-challenge commit",
			arrange: func(t *testing.T, h *harness, c *capture) {
				advanceExactly(t, c, 5)
				ambiguous := &ambiguousTransactor{inner: h.Coordinator.Transactor}
				h.Coordinator.Transactor = ambiguous
				ambiguous.Skip, ambiguous.LoseNext = 1, true
				if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
					t.Fatalf("the lost challenge commit was not reported: %v", err)
				}
				h.Coordinator.Transactor = ambiguous.inner
			},
		},
	}

	for _, half := range halves {
		t.Run(half.name, func(t *testing.T) {
			h := newHarness(t)
			c := h.admit(t).hold(t).finish(t, true)

			half.arrange(t, h, c)

			c.expireTheLease(t)

			// The second process. A new owner id is exactly what an ATC
			// restart produces, and it is the whole difference between the two
			// owners.
			h.Coordinator.OwnerID = uuid.NewString()

			taken := c.advance(t)

			record := c.record(t)
			if record.State != output.CaptureStateRegistered {
				t.Fatalf("a capture inherited by a new owner is %s after %v", record.State, taken)
			}
			if record.CaptureFence != 2 {
				t.Errorf("the capture is admitted under fence %d and the takeover advanced the "+
					"lease to 2; one fence source or none", record.CaptureFence)
			}
			if record.Receipt == nil {
				t.Fatal("the new owner published an object and could never obtain a receipt for it")
			}
			// THE TWO FENCES ARE NOT ONE NUMBER, and a takeover is where
			// that stops being a distinction without a difference. The capture
			// fence moved 1 -> 2 above: the new owner owns the capture. The
			// WRITER fence did not move and must not have -- nobody took the
			// source incarnation over, the tickets the original Pod holds are
			// still the tickets that exist, and the node's ledger says so. This
			// assertion read `== 2` while the receipt's writer fence was a cast
			// of the capture fence the control plane itself supplied, so it was
			// the conflation written down as an expectation.
			if record.Receipt.Claims.WriterFence != output.FirstWriterFence {
				t.Errorf("the receipt is bound to writer fence %d after a CAPTURE takeover; the "+
					"capture fence moved to %d and the writer fence is a different claim, about "+
					"who was admitted to write the source",
					record.Receipt.Claims.WriterFence, record.CaptureFence)
			}
			if output.WriterFence(record.CaptureFence) == record.Receipt.Claims.WriterFence {
				t.Error("the capture fence and the writer fence are the same number after a " +
					"takeover, so this case cannot tell one from the other")
			}
			if keys := h.bucketKeys(t); len(keys) != 1 {
				t.Errorf("the takeover created %d object(s): %v", len(keys), keys)
			}
			if h.Dialer.Calls("begin-seal") != 1 {
				t.Errorf("the seal was begun %d times across the takeover; the captured drain "+
					"set is captured once", h.Dialer.Calls("begin-seal"))
			}
			if !record.ReleaseAcknowledged {
				t.Error("the new owner registered the receipt and never released the source it " +
					"inherited")
			}
			if permitted, reason := hangaroutput.TerminalExposurePermitted(record); !permitted {
				t.Errorf("a capture completed by its new owner does not permit a terminal "+
					"outcome: %s", reason)
			}
		})
	}
}

// advanceExactly drives the coordinator forward a fixed number of transitions
// and refuses to let a failure look like an arrangement.
// stampAnExpiredSealDeadline puts a past deadline on the reservation BEFORE
// begin_seal composes one.
//
// It is a raw write, on purpose, and it replaced the arrangement these specs
// used to make: Coordinator.SealDeadline set to a negative duration. That field
// is Req 17's only configuration site and it is bounded now -- 30 seconds
// through 30 minutes -- so "already expired" can no longer be composed through
// it, and it should not be: a deployment able to configure a deadline in the
// past is the defect the bound exists to stop. What these specs are about is
// what happens once the deadline HAS passed, which is a fact about the row.
//
// It reproduces the old arrangement exactly rather than approximating it,
// because RecordSealDeadline coalesces: a deadline already on the row is the
// one begin_seal keeps and the one it hands the node. So both clocks see the
// same expired instant, which is what they saw before.
func stampAnExpiredSealDeadline(t *testing.T, h *harness, c *capture, by time.Duration) {
	t.Helper()

	if _, err := h.Conn.Exec(`
		UPDATE hangar_capture_reservations SET seal_deadline_at = now() - $2::interval
		WHERE reservation_id = $1`, string(c.record(t).ReservationID), by.String()); err != nil {
		t.Fatalf("stamping an expired seal deadline: %v", err)
	}
}

func advanceExactly(t *testing.T, c *capture, transitions int) {
	t.Helper()

	for i := 0; i < transitions; i++ {
		decision, err := c.advanceOnce(t)
		if err != nil {
			t.Fatalf("arranging (%s), transition %d of %d: %v", decision.Transition, i+1,
				transitions, err)
		}
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
