package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// The execution ledger is the node's sole runtime owner of exact execution
// truth. These are stdlib tests against a real directory, because every
// interesting case here is a fact about a file: what survives a crash between
// the fsync and the rename, what a restart reads back, what a torn record does
// to readiness. A double would prove the double.
//
// Narrowed by decision F13: what is wired here is a capture-selected execution.
// The record shape must stay able to hold a base execution with no capture
// extension, and TestABaseExecutionNeedsNoExtensionToBeComplete is the fixture
// that says so; the sibling track exact_execution_control adds the callers.

const (
	testExecution = executioncontrol.ExecutionID("33333333-3333-4333-8333-333333333333")
	testNode      = executioncontrol.NodeUID("node-1")
	testPod       = executioncontrol.PodUID("pod-1")
	testEpoch     = executioncontrol.ActivationEpoch(7)
)

type ledgerFixture struct {
	ledger  *ExecutionLedger
	store   *controlStore
	dir     string
	public  ed25519.PublicKey
	private ed25519.PrivateKey
	signer  *executioncontrol.AcknowledgementSigner
	now     time.Time
}

func (fixture *ledgerFixture) clock() time.Time { return fixture.now }

// fixedNow is the one instant these tests run at. A ledger's timestamps are
// what a signature covers, so a clock that moved would make two statements over
// the same facts differ for a reason no assertion is about.
func fixedNow() time.Time { return time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC) }

func newLedger(t *testing.T) *ledgerFixture {
	t.Helper()

	fixture := &ledgerFixture{
		dir: t.TempDir(),
		now: fixedNow(),
	}
	fixture.reopen(t)

	return fixture
}

// reopen is a daemon restart: the process is gone, the directory is not.
func (fixture *ledgerFixture) reopen(t *testing.T) {
	t.Helper()

	if fixture.store != nil {
		_ = fixture.store.Close()
	}
	if fixture.public == nil {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generating the control key: %v", err)
		}
		signer, err := executioncontrol.NewAcknowledgementSigner(private)
		if err != nil {
			t.Fatalf("building the signer: %v", err)
		}
		fixture.public, fixture.private = public, private
		fixture.signer = signer
	}

	store, err := openControlStore(fixture.dir)
	if err != nil {
		t.Fatalf("opening the control store: %v", err)
	}
	quarantined, err := store.validate()
	if err != nil {
		t.Fatalf("validating the control store: %v", err)
	}
	if len(quarantined) != 0 {
		t.Fatalf("the control store quarantined %v at open", quarantined)
	}

	ledger, err := OpenExecutionLedger(store, testNode, testEpoch, fixture.signer, fixture.clock)
	if err != nil {
		t.Fatalf("opening the execution ledger: %v", err)
	}
	fixture.store, fixture.ledger = store, ledger
}

// sameStatement compares two acknowledgements by the bytes their signature
// covers plus the signature itself.
//
// Not `==`: Acknowledgement carries *ExitOutcome, so the operator compares
// pointers and two identical statements read as different. Comparing the signed
// bytes is also the stronger claim -- it is exactly what a verifier compares,
// so "the same statement" means the same thing here as it does to a control
// plane.
func sameStatement(left, right executioncontrol.Acknowledgement) bool {
	unsigned := func(ack executioncontrol.Acknowledgement) string {
		ack.Signature = ""

		return string(executioncontrol.CanonicalAcknowledgementBytes(ack))
	}

	return left.Signature == right.Signature && unsigned(left) == unsigned(right)
}

func describeStatement(ack executioncontrol.Acknowledgement) string {
	raw, _ := json.Marshal(ack)

	return string(raw)
}

func identity(fence executioncontrol.Fence) executioncontrol.Identity {
	return executioncontrol.Identity{ExecutionID: testExecution, Fence: fence}
}

func envelope(fence executioncontrol.Fence) executioncontrol.Envelope {
	return executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity(fence),
		ActivationEpoch: testEpoch,
		NodeUID:         testNode,
		PodUID:          testPod,
		Capability:      "opaque-capability",
	}
}

func admitted(t *testing.T, fixture *ledgerFixture) {
	t.Helper()

	if err := fixture.ledger.Admit(envelope(1)); err != nil {
		t.Fatalf("admitting: %v", err)
	}
}

// The whole base state machine, in the order a real execution walks it.
func TestTheLedgerWalksOneExecutionFromAdmissionToAnAuthoritativeFinish(t *testing.T) {
	fixture := newLedger(t)

	// Before admission there is nothing to classify, and a start is refused:
	// the supervisor writes its start record before launching the child, and a
	// start the control plane never admitted is a command nobody authorized.
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("an unadmitted execution was started: %v", err)
	}

	admitted(t, fixture)

	// An outcome for a command with no durable start record is an inference,
	// not an observation: nothing on this node ever saw the process, so
	// "it exited zero" is a claim the caller made about itself.
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish,
		executioncontrol.ExitOutcome{ExitCode: 0}); !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("an outcome was recorded for a command that never started: %v", err)
	}

	// Admitted and not started is never_started, and it is the only
	// classification a first start is admissible from.
	result, err := fixture.ledger.Classify(identity(1))
	if err != nil {
		t.Fatalf("classifying: %v", err)
	}
	if result.Classification != executioncontrol.ClassificationNeverStarted {
		t.Errorf("an admitted, unstarted execution classified %s", result.Classification)
	}
	if result.Acknowledgement != nil {
		t.Error("a never_started classification carried an acknowledgement")
	}

	start, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1")
	if err != nil {
		t.Fatalf("recording the start: %v", err)
	}
	if start.Kind != executioncontrol.AcknowledgementStart {
		t.Errorf("the start acknowledgement is a %s", start.Kind)
	}
	if err := executioncontrol.VerifyAcknowledgement(start, fixture.public); err != nil {
		t.Errorf("the start acknowledgement does not verify: %v", err)
	}

	if result, err = fixture.ledger.Classify(identity(1)); err != nil {
		t.Fatalf("classifying: %v", err)
	}
	if result.Classification != executioncontrol.ClassificationExecuting {
		t.Errorf("a started execution classified %s", result.Classification)
	}

	// Observing before there is an outcome says so; it does not wait forever
	// and it does not invent one.
	observed, err := fixture.ledger.Observe(identity(1))
	if err != nil {
		t.Fatalf("observing: %v", err)
	}
	if observed.Acknowledgement != nil {
		t.Error("observing an executing command returned an acknowledgement")
	}

	finish, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
	if err != nil {
		t.Fatalf("recording the finish: %v", err)
	}
	if err := executioncontrol.VerifyAcknowledgement(finish, fixture.public); err != nil {
		t.Errorf("the finish acknowledgement does not verify: %v", err)
	}
	if finish.LedgerSequence <= start.LedgerSequence {
		t.Errorf("the finish is sequence %d and the start was %d; the ledger sequence is monotonic",
			finish.LedgerSequence, start.LedgerSequence)
	}

	if result, err = fixture.ledger.Classify(identity(1)); err != nil {
		t.Fatalf("classifying: %v", err)
	}
	if result.Classification != executioncontrol.ClassificationAuthoritativeFinish {
		t.Errorf("a finished execution classified %s", result.Classification)
	}
	if result.Acknowledgement == nil || !sameStatement(*result.Acknowledgement, finish) {
		t.Error("the authoritative classification does not carry the acknowledgement that makes it authoritative")
	}
	if observed, err = fixture.ledger.Observe(identity(1)); err != nil {
		t.Fatalf("observing: %v", err)
	}
	if observed.Acknowledgement == nil || !sameStatement(*observed.Acknowledgement, finish) {
		t.Error("ObserveFinishOrStop returned something other than the durable acknowledgement")
	}
}

func TestTheLedgerRecordsARequestedStopAsAnInterruptionAndNotASuccess(t *testing.T) {
	fixture := newLedger(t)
	admitted(t, fixture)
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}

	stop, err := fixture.ledger.RequestStop(identity(1))
	if err != nil {
		t.Fatalf("requesting the stop: %v", err)
	}
	if !stop.Accepted {
		t.Error("a stop of an executing command was refused")
	}
	// Accepted is not an outcome: the classification is still executing until
	// the supervisor's own record arrives.
	if stop.Classification != executioncontrol.ClassificationExecuting {
		t.Errorf("the accepted stop reported classification %s", stop.Classification)
	}

	ack, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementStop,
		executioncontrol.ExitOutcome{ExitCode: 143, Signalled: true, Signal: "TERM"})
	if err != nil {
		t.Fatalf("recording the stop outcome: %v", err)
	}
	if ack.Outcome.Successful() {
		t.Error("a signalled stop was recorded as a successful outcome")
	}

	result, err := fixture.ledger.Classify(identity(1))
	if err != nil {
		t.Fatalf("classifying: %v", err)
	}
	if result.Classification != executioncontrol.ClassificationAuthoritativeStop {
		t.Errorf("a stopped execution classified %s", result.Classification)
	}

	// A second stop of a terminal execution is refused rather than accepted
	// into nothing.
	again, err := fixture.ledger.RequestStop(identity(1))
	if err != nil {
		t.Fatalf("requesting a second stop: %v", err)
	}
	if again.Accepted {
		t.Error("a stop was admitted for an execution that had already stopped")
	}
}

func TestUnresolvedAndLostAreNeverRoundedToAnOutcome(t *testing.T) {
	for _, row := range []struct {
		name     string
		mark     func(*ExecutionLedger) error
		expect   executioncontrol.Classification
		terminal bool
	}{
		{
			name: "the answer may still arrive",
			mark: func(l *ExecutionLedger) error {
				return l.MarkUnresolved(identity(1), "the supervisor is not answering")
			},
			expect: executioncontrol.ClassificationUnresolved,
		},
		{
			name:     "the ledger that would hold the answer is gone",
			mark:     func(l *ExecutionLedger) error { return l.MarkLost(identity(1), "the node is gone") },
			expect:   executioncontrol.ClassificationLost,
			terminal: true,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			fixture := newLedger(t)
			admitted(t, fixture)
			if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
				t.Fatalf("starting: %v", err)
			}
			if err := row.mark(fixture.ledger); err != nil {
				t.Fatalf("marking: %v", err)
			}

			result, err := fixture.ledger.Classify(identity(1))
			if err != nil {
				t.Fatalf("classifying: %v", err)
			}
			if result.Classification != row.expect {
				t.Errorf("classified %s, expected %s", result.Classification, row.expect)
			}
			if result.Acknowledgement != nil {
				t.Error("a non-authoritative classification carried an acknowledgement; " +
					"evidence attached to an unproved answer is how an inference starts " +
					"looking like proof")
			}
			if result.Classification.Terminal() != row.terminal {
				t.Errorf("%s reports terminal=%v", result.Classification, result.Classification.Terminal())
			}

			// Neither may authorize destroying anything.
			eligible, err := fixture.ledger.CleanupEligible(identity(1))
			if err != nil {
				t.Fatalf("asking about cleanup: %v", err)
			}
			if eligible.Eligible {
				t.Errorf("cleanup was permitted on classification %s", result.Classification)
			}
			if eligible.WithheldReason == "" {
				t.Error("cleanup was withheld with no reason")
			}
		})
	}
}

func TestTheLedgerRefusesAForeignIdentityOrAStaleFence(t *testing.T) {
	fixture := newLedger(t)
	admitted(t, fixture)

	// The control, first: the admitted identity is served.
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("the admitted identity was refused: %v", err)
	}

	other := executioncontrol.Identity{
		ExecutionID: "44444444-4444-4444-8444-444444444444",
		Fence:       1,
	}
	if _, err := fixture.ledger.Classify(other); err != nil {
		t.Fatalf("classifying an unknown execution should answer, not fail: %v", err)
	} else if result, _ := fixture.ledger.Classify(other); result.Classification != executioncontrol.ClassificationNeverStarted {
		t.Errorf("an execution this node never admitted classified %s", result.Classification)
	}

	// A stale fence is refused for every operation that acts, and the current
	// one is still served afterwards.
	stale := identity(0)
	for name, act := range map[string]func() error{
		"start": func() error {
			_, err := fixture.ledger.RecordStart(stale, testPod, "proc-1")

			return err
		},
		"outcome": func() error {
			_, err := fixture.ledger.RecordOutcome(stale, executioncontrol.AcknowledgementFinish,
				executioncontrol.ExitOutcome{})

			return err
		},
		"stop": func() error {
			_, err := fixture.ledger.RequestStop(stale)

			return err
		},
	} {
		if err := act(); !errors.Is(err, executioncontrol.ErrStaleFence) &&
			!errors.Is(err, output.ErrUnauthorized) && !errors.Is(err, executioncontrol.ErrInvalidIdentity) {
			t.Errorf("a stale fence was admitted for %s: %v", name, err)
		}
	}

	// A fence that has been superseded, rather than one that is malformed.
	if err := fixture.ledger.Admit(envelope(2)); err != nil {
		t.Fatalf("taking the execution over at a higher fence: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{}); !errors.Is(err, executioncontrol.ErrStaleFence) {
		t.Errorf("the superseded fence was still admitted: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(2),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{}); err != nil {
		t.Errorf("the current fence was refused: %v", err)
	}
}

// Idempotent replay and its conflict twin.
//
// "Repeating the call returns the same answer" passes for a ledger that ignores
// what it is told and answers from thin air, so the twin has to show that the
// same identity offered a DIFFERENT outcome is a typed conflict and that the
// first answer is still the one in force.
func TestARepeatedOutcomeReturnsTheSameStatementAndAConflictingOneIsRefused(t *testing.T) {
	fixture := newLedger(t)
	admitted(t, fixture)
	started, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1")
	if err != nil {
		t.Fatalf("starting: %v", err)
	}

	// The start replays too, and for the same reason: the supervisor writes it
	// before launching the child, so the call that never returned is exactly the
	// one a caller has to be able to repeat.
	startedAgain, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1")
	if err != nil {
		t.Fatalf("replaying the start: %v", err)
	}
	if !sameStatement(startedAgain, started) {
		t.Errorf("the replayed start returned a different statement:\n first: %s\nreplay: %s",
			describeStatement(started), describeStatement(startedAgain))
	}
	// And a start of the same execution as a DIFFERENT process is a conflict,
	// not a second start: that is a command about to run twice.
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-2"); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a second, different start was admitted: %v", err)
	}

	first, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
	if err != nil {
		t.Fatalf("recording: %v", err)
	}

	// The ambiguous client response: the caller never saw the answer and asks
	// again. It gets the same statement, byte for byte -- not a new sequence
	// and not a new signature.
	replay, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
	if err != nil {
		t.Fatalf("replaying: %v", err)
	}
	if !sameStatement(replay, first) {
		t.Errorf("the replay returned a different statement:\n first: %s\nreplay: %s",
			describeStatement(first), describeStatement(replay))
	}

	for name, conflicting := range map[string]struct {
		kind    executioncontrol.AcknowledgementKind
		outcome executioncontrol.ExitOutcome
	}{
		"a different exit code": {executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 1}},
		"a signal instead":      {executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{Signalled: true, Signal: "KILL"}},
		"a stop instead":        {executioncontrol.AcknowledgementStop, executioncontrol.ExitOutcome{ExitCode: 143, Signalled: true, Signal: "TERM"}},
	} {
		if _, err := fixture.ledger.RecordOutcome(identity(1), conflicting.kind, conflicting.outcome); !errors.Is(err, output.ErrConflict) {
			t.Errorf("%s overwrote a recorded outcome: %v", name, err)
		}
	}

	// And the first answer still stands.
	result, err := fixture.ledger.Classify(identity(1))
	if err != nil {
		t.Fatalf("classifying: %v", err)
	}
	if result.Acknowledgement == nil || !sameStatement(*result.Acknowledgement, first) {
		t.Error("a refused conflicting outcome changed what the ledger says")
	}
}

// Restart recovery is the requirement that matters most here: if ATC loses the
// remote-exec response, recovery reads the exact supervisor record and reports
// the same outcome. It never invokes the command again.
func TestARestartReturnsTheSameSignedStatementWithoutRelaunchingAnything(t *testing.T) {
	fixture := newLedger(t)
	admitted(t, fixture)
	start, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1")
	if err != nil {
		t.Fatalf("starting: %v", err)
	}
	finish, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
	if err != nil {
		t.Fatalf("finishing: %v", err)
	}

	fixture.reopen(t)

	observed, err := fixture.ledger.Observe(identity(1))
	if err != nil {
		t.Fatalf("observing after a restart: %v", err)
	}
	if observed.Acknowledgement == nil {
		t.Fatal("a restarted daemon has no acknowledgement for a finished execution")
	}
	if !sameStatement(*observed.Acknowledgement, finish) {
		t.Errorf("a restarted daemon returned a different statement:\nbefore: %s\n after: %s",
			describeStatement(finish), describeStatement(*observed.Acknowledgement))
	}
	if err := executioncontrol.VerifyAcknowledgement(*observed.Acknowledgement, fixture.public); err != nil {
		t.Errorf("the recovered statement does not verify: %v", err)
	}

	// A start after a restart of an already-finished execution is refused. This
	// is the "never invokes the command again" half stated where the ledger can
	// enforce it.
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-2"); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a finished execution was started again after a restart: %v", err)
	}

	// And the sequence keeps climbing rather than restarting from one, so two
	// statements from one node can always be ordered.
	if err := fixture.ledger.Admit(executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        executioncontrol.Identity{ExecutionID: "44444444-4444-4444-8444-444444444444", Fence: 1},
		ActivationEpoch: testEpoch,
		NodeUID:         testNode,
		PodUID:          testPod,
		Capability:      "opaque-capability",
	}); err != nil {
		t.Fatalf("admitting a second execution: %v", err)
	}
	next, err := fixture.ledger.RecordStart(
		executioncontrol.Identity{ExecutionID: "44444444-4444-4444-8444-444444444444", Fence: 1},
		testPod, "proc-3")
	if err != nil {
		t.Fatalf("starting the second execution: %v", err)
	}
	if next.LedgerSequence <= finish.LedgerSequence {
		t.Errorf("after a restart the sequence went back to %d; the last before it was %d "+
			"(start was %d)", next.LedgerSequence, finish.LedgerSequence, start.LedgerSequence)
	}
}

// The crash halves. Every one of these is a point in the durable write, and the
// rule is the same at all of them: what a reader sees afterwards is the whole
// new record or the whole old one.
func TestACrashDuringADurableWriteLeavesTheOldRecordOrTheNewOneAndNeverHalf(t *testing.T) {
	for _, stage := range []faultStage{
		faultBeforeTempWrite, faultAfterTempWrite, faultAfterTempFsync, faultAfterRename,
	} {
		t.Run(string(stage), func(t *testing.T) {
			fixture := newLedger(t)
			admitted(t, fixture)
			start, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1")
			if err != nil {
				t.Fatalf("starting: %v", err)
			}

			fixture.store.fault = func(at faultStage) error {
				if at == stage {
					return errInjectedCrash
				}

				return nil
			}
			_, err = fixture.ledger.RecordOutcome(identity(1),
				executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
			crashedBeforeRename := stage != faultAfterRename
			if crashedBeforeRename && !errors.Is(err, errInjectedCrash) {
				t.Fatalf("the injected crash at %s was not reported: %v", stage, err)
			}
			fixture.store.fault = nil

			// A restart is the only reader that matters: the process that
			// crashed has no memory left.
			fixture.reopen(t)

			result, err := fixture.ledger.Classify(identity(1))
			if err != nil {
				t.Fatalf("classifying after a crash at %s: %v", stage, err)
			}
			switch stage {
			case faultAfterRename:
				// The record is in place; only the directory entry was not
				// fsynced. A reader either sees it or does not, and on this
				// filesystem it does -- what must never happen is a torn one.
				if result.Classification != executioncontrol.ClassificationAuthoritativeFinish {
					t.Errorf("a crash after the rename lost the record: %s", result.Classification)
				}
			default:
				if result.Classification != executioncontrol.ClassificationExecuting {
					t.Errorf("a crash at %s left classification %s; the previous record must "+
						"still be the whole truth", stage, result.Classification)
				}
				if observed, _ := fixture.ledger.Observe(identity(1)); observed.Acknowledgement != nil {
					t.Errorf("a crash at %s produced an acknowledgement", stage)
				}
			}
			_ = start

			// And the execution can still be finished afterwards, which is what
			// makes the crash recoverable rather than merely non-corrupting.
			if stage != faultAfterRename {
				if _, err := fixture.ledger.RecordOutcome(identity(1),
					executioncontrol.AcknowledgementFinish,
					executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
					t.Errorf("the execution could not be finished after a crash at %s: %v", stage, err)
				}
			}
		})
	}
}

func TestATornOrUnsupportedRecordQuarantinesAndKeepsTheDaemonUnready(t *testing.T) {
	for name, corrupt := range map[string]func(path string) error{
		"a truncated record": func(path string) error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			return os.WriteFile(path, raw[:len(raw)/2], 0o600)
		},
		"a record whose checksum does not match its body": func(path string) error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var record controlRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			record.Body = append(record.Body[:len(record.Body)-1], []byte(`,"tampered":true}`)...)
			edited, err := json.Marshal(record)
			if err != nil {
				return err
			}

			return os.WriteFile(path, edited, 0o600)
		},
		"a record from a newer daemon": func(path string) error {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var record controlRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return err
			}
			record.RecordVersion = "hangar-output-control-record-v2"
			edited, err := json.Marshal(record)
			if err != nil {
				return err
			}

			return os.WriteFile(path, edited, 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newLedger(t)
			admitted(t, fixture)
			if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
				t.Fatalf("starting: %v", err)
			}

			names, err := fixture.store.names()
			if err != nil || len(names) == 0 {
				t.Fatalf("the ledger wrote no record: %v %v", names, err)
			}
			control := filepath.Join(fixture.dir, ControlDirName, names[0])
			if err := corrupt(control); err != nil {
				t.Fatalf("corrupting: %v", err)
			}

			_ = fixture.store.Close()
			store, err := openControlStore(fixture.dir)
			if err != nil {
				t.Fatalf("reopening: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			quarantined, err := store.validate()
			if err != nil {
				t.Fatalf("validating: %v", err)
			}
			if len(quarantined) == 0 {
				t.Fatalf("%s was accepted at startup", name)
			}

			// And a further restart still reports it: a corrupt ledger that is
			// cleared by restarting is a corrupt ledger nobody ever sees.
			again, err := openControlStore(fixture.dir)
			if err != nil {
				t.Fatalf("reopening again: %v", err)
			}
			defer again.Close()
			stillQuarantined, err := again.validate()
			if err != nil {
				t.Fatalf("validating again: %v", err)
			}
			if len(stillQuarantined) == 0 {
				t.Errorf("%s stopped being reported after a restart", name)
			}
		})
	}
}

// Destructive cleanup fails closed, and the extension gates are opaque names.
//
// The permitted case is asserted FIRST. "Not eligible" passes on a ledger that
// refuses everything, so the positive control has to be the line above it.
func TestDestructiveCleanupWaitsForTheOutcomeAndForEveryOpenGate(t *testing.T) {
	fixture := newLedger(t)
	admitted(t, fixture)
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if _, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0}); err != nil {
		t.Fatalf("finishing: %v", err)
	}

	// The control: a finished execution with no gates open is eligible.
	eligible, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking: %v", err)
	}
	if !eligible.Eligible {
		t.Fatalf("a finished execution with no open gate was refused cleanup: %q", eligible.WithheldReason)
	}

	// Now the absence: an open gate withholds it, by opaque name.
	if err := fixture.ledger.OpenGate(identity(1), "source-hold"); err != nil {
		t.Fatalf("opening a gate: %v", err)
	}
	if eligible, err = fixture.ledger.CleanupEligible(identity(1)); err != nil {
		t.Fatalf("asking: %v", err)
	}
	if eligible.Eligible {
		t.Error("cleanup was permitted with a gate still open")
	}
	if len(eligible.OpenExtensionGates) != 1 || eligible.OpenExtensionGates[0] != "source-hold" {
		t.Errorf("the open gates are %v", eligible.OpenExtensionGates)
	}
	if err := fixture.ledger.CloseGate(identity(1), "source-hold"); err != nil {
		t.Fatalf("closing the gate: %v", err)
	}
	if eligible, err = fixture.ledger.CleanupEligible(identity(1)); err != nil {
		t.Fatalf("asking: %v", err)
	}
	if !eligible.Eligible {
		t.Errorf("cleanup stayed refused after the last gate closed: %q", eligible.WithheldReason)
	}

	// And before any outcome, an execution with no gate at all is still
	// refused: the finish acknowledgement is the other half.
	second := executioncontrol.Identity{ExecutionID: "44444444-4444-4444-8444-444444444444", Fence: 1}
	if err := fixture.ledger.Admit(executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        second,
		ActivationEpoch: testEpoch,
		NodeUID:         testNode,
		PodUID:          testPod,
		Capability:      "opaque-capability",
	}); err != nil {
		t.Fatalf("admitting: %v", err)
	}
	if _, err := fixture.ledger.RecordStart(second, testPod, "proc-2"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	if eligible, err = fixture.ledger.CleanupEligible(second); err != nil {
		t.Fatalf("asking: %v", err)
	}
	if eligible.Eligible {
		t.Error("cleanup was permitted for an execution with no finish acknowledgement")
	}
}

// Decision F13's contract obligation, as a fixture rather than a promise.
//
// The record and acknowledgement shapes frozen here must already be able to
// hold a base execution that carries no capture extension. The sibling track
// exact_execution_control adds the callers; this is the assertion that it will
// not have to change the shape to do it.
func TestABaseExecutionNeedsNoExtensionToBeComplete(t *testing.T) {
	fixture := newLedger(t)
	admitted(t, fixture)
	if _, err := fixture.ledger.RecordStart(identity(1), testPod, "proc-1"); err != nil {
		t.Fatalf("starting: %v", err)
	}
	finish, err := fixture.ledger.RecordOutcome(identity(1),
		executioncontrol.AcknowledgementFinish, executioncontrol.ExitOutcome{ExitCode: 0})
	if err != nil {
		t.Fatalf("finishing: %v", err)
	}

	// Complete on its own: authoritative, verifiable, and cleanup-eligible with
	// no extension having been involved at any point.
	if err := executioncontrol.VerifyAcknowledgement(finish, fixture.public); err != nil {
		t.Fatalf("the base acknowledgement does not verify: %v", err)
	}
	eligible, err := fixture.ledger.CleanupEligible(identity(1))
	if err != nil {
		t.Fatalf("asking: %v", err)
	}
	if !eligible.Eligible {
		t.Errorf("a base execution with no extension was refused cleanup: %q", eligible.WithheldReason)
	}

	// And it carries no extension field. This is the wire half: a base
	// acknowledgement that mentioned a hold, a capture, a source lease or an
	// output would be the extension leaking into the base truth.
	encoded, err := json.Marshal(finish)
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	for _, forbidden := range []string{
		"capture", "handoff", "source_lease", "handle_generation", "output", "hold",
		"writer_ticket", "writer_fence", "incarnation", "receipt", "scope", "digest",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("a base acknowledgement carries %q: %s", forbidden, encoded)
		}
	}

	// The same for the record on disk, which is the thing the sibling track
	// would have to change if the shape were wrong.
	names, err := fixture.store.names()
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(fixture.dir, ControlDirName, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for _, forbidden := range []string{"capture", "handoff", "source_lease", "incarnation"} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("the base execution record %s carries %q: %s", name, forbidden, raw)
			}
		}
	}
}
