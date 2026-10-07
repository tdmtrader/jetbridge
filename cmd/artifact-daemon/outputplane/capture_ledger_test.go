package outputplane

import (
	"context"
	"io/fs"

	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/ledger"
)

// The capture ledger, over real directories, a real control store, a real
// canonicalizer and a fake-gcs object store. Pod termination is the one
// thing driven by the test: it is the fact this file exists to wait for.

const testOutput = output.OutputName("result")

// heldPods is the PodTerminations a test drives: every Pod is running until
// the test says it has stopped.
type heldPods struct {
	mu      sync.Mutex
	stopped map[executioncontrol.PodUID]bool
	asked   atomic.Int64
}

func newHeldPods() *heldPods { return &heldPods{stopped: map[executioncontrol.PodUID]bool{}} }

func (pods *heldPods) stop(pod executioncontrol.PodUID) {
	pods.mu.Lock()
	defer pods.mu.Unlock()
	pods.stopped[pod] = true
}

func (pods *heldPods) Terminated(_ context.Context, pod executioncontrol.PodUID) (bool, error) {
	pods.asked.Add(1)
	pods.mu.Lock()
	defer pods.mu.Unlock()

	return pods.stopped[pod], nil
}

type captureFixture struct {
	ledgerFixture

	daemon  *Daemon
	config  Config
	pods    *heldPods
	capture *CaptureLedger

	objects *fakestorage.Server
	bucket  string
}

func newCaptureLedger(t *testing.T) *captureFixture {
	t.Helper()

	fixture := &captureFixture{ledgerFixture: *newLedger(t), pods: newHeldPods()}
	server, bucket := emulator(t)
	fixture.objects, fixture.bucket = server, bucket
	fixture.config = validConfig(t, server.URL(), bucket)
	fixture.config.ControlKeyFile = writePrivateKey(t, fixture.private)
	fixture.config.ActivationEpoch = uint64(testEpoch)
	daemon, err := Build(t.Context(), fixture.config)
	if err != nil {
		t.Fatalf("building the daemon: %v", err)
	}
	fixture.daemon = daemon
	fixture.openCapture(t)

	return fixture
}

func (fixture *captureFixture) openCapture(t *testing.T) {
	t.Helper()

	capture, err := OpenCaptureLedger(fixture.store, fixture.ledger, fixture.daemon, CaptureLedgerConfig{
		Node: testNode, StepsDir: fixture.stepsDir(), ScratchDir: fixture.config.ScratchDir,
		Terminations: fixture.pods, SealWait: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("opening the capture ledger: %v", err)
	}
	capture.poll = 10 * time.Millisecond
	fixture.capture = capture
	t.Cleanup(func() { _ = capture.Close() })
}

func (fixture *captureFixture) stepsDir() string { return filepath.Join(fixture.dir, "steps") }

// restart is a daemon restart: the process is gone, the directories are not.
func (fixture *captureFixture) restart(t *testing.T) {
	t.Helper()

	_ = fixture.capture.Close()
	fixture.ledgerFixture.reopen(t)
	fixture.openCapture(t)
}

func (fixture *captureFixture) key() output.CaptureKey {
	return output.CaptureKey{ExecutionID: testExecution, Output: testOutput}
}

func (fixture *captureFixture) stepDir() string {
	return filepath.Join(fixture.stepsDir(), fixture.key().Directory())
}

func holdRequest() output.CaptureHoldRequest {
	return output.CaptureHoldRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput, PodUID: testPod,
	}
}

func sealRequest() output.CaptureSealRequest {
	return output.CaptureSealRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput, PodUID: testPod,
	}
}

// sealNow drives the asynchronous seal to its answer: it asks again while the
// node says the seal is in progress, the way the coordinator does.
func sealNow(t *testing.T, fixture *captureFixture) (output.CaptureSealResult, error) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		result, err := fixture.capture.Seal(context.Background(), sealRequest(), nil)
		if !errors.Is(err, output.ErrSealInProgress) || time.Now().After(deadline) {
			return result, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// publishNow drives the asynchronous publish to its answer.
func publishNow(t *testing.T, fixture *captureFixture, request output.CapturePublishRequest) (output.CapturePublishResult, error) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for {
		result, err := fixture.capture.Publish(context.Background(), request, nil)
		if !errors.Is(err, output.ErrPublishInProgress) || time.Now().After(deadline) {
			return result, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func held(t *testing.T, fixture *captureFixture) output.CaptureHoldAcknowledgement {
	t.Helper()

	admitted(t, &fixture.ledgerFixture)
	ack, err := fixture.capture.Hold(context.Background(), holdRequest())
	if err != nil {
		t.Fatalf("holding: %v", err)
	}

	return ack
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheHoldWritesADurableMarkerTheSweeperRespects(t *testing.T) {
	fixture := newCaptureLedger(t)
	classifier := ledger.New(fixture.dir)
	if class := classifier.Classify(fixture.key().Directory()); class != ledger.Unmanaged {
		t.Fatalf("before any hold the step directory is %s", class)
	}

	ack := held(t, fixture)
	if ack.Kind != output.HoldAcknowledged || ack.Marker.State != output.StepHeld ||
		ack.Marker.Node != testNode || ack.Marker.PodUID != testPod {
		t.Fatalf("the hold answered %+v", ack)
	}
	if info, err := os.Stat(fixture.stepDir()); err != nil || !info.IsDir() {
		t.Fatalf("the hold did not leave a step directory: %v", err)
	}

	// Durable: a restart reads the same marker back, and the classifier the
	// artifact daemon's destructive paths consult answers held.
	fixture.restart(t)
	for _, path := range []string{fixture.key().Directory(), string(testExecution) + ".capture", ""} {
		if class := classifier.Classify(path); class != ledger.Held {
			t.Errorf("after a restart %q is %s, want held", path, class)
		}
	}
	if !fixture.ledger.gateOpen(t, testExecution, SourceHoldGate) {
		t.Error("the hold did not open the base execution's cleanup gate")
	}

	// Idempotent for the same Pod, refused for another.
	if _, err := fixture.capture.Hold(context.Background(), holdRequest()); err != nil {
		t.Errorf("a repeated hold for the same Pod was refused: %v", err)
	}
	other := holdRequest()
	other.PodUID = "pod-2"
	if _, err := fixture.capture.Hold(context.Background(), other); !errors.Is(err, output.ErrConflict) {
		t.Errorf("a hold from a recreated Pod answered %v, want a conflict", err)
	}
}

func TestAHoldForAnExecutionThisNodeNeverAdmittedIsRefused(t *testing.T) {
	fixture := newCaptureLedger(t)
	_, err := fixture.capture.Hold(context.Background(), holdRequest())
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Fatalf("a hold with no admission answered %v", err)
	}
	if _, err := os.Stat(fixture.stepDir()); !os.IsNotExist(err) {
		t.Errorf("a refused hold left a step directory: %v", err)
	}
}

// A3, node-local half: a directory with no held marker is never captured,
// however much it holds.
func TestASealWithNoHeldMarkerIsRefusedAndPublishesNothing(t *testing.T) {
	fixture := newCaptureLedger(t)
	admitted(t, &fixture.ledgerFixture)
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "written without a hold")
	fixture.pods.stop(testPod)

	_, err := fixture.capture.Seal(context.Background(), sealRequest(), nil)
	if !errors.Is(err, output.ErrNotFound) {
		t.Fatalf("a seal with no marker answered %v, want not found", err)
	}
	_, err = publishNow(t, fixture, output.CapturePublishRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
		Digest: hangar.Digest("sha256:" + string(make64('a'))),
	})
	if !errors.Is(err, output.ErrNotFound) {
		t.Fatalf("a publish with no marker answered %v, want not found", err)
	}
}

func make64(c byte) []byte {
	out := make([]byte, 64)
	for i := range out {
		out[i] = c
	}
	return out
}

func TestASealOfAnotherPodsMarkerIsRefused(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	request := sealRequest()
	request.PodUID = "pod-other"
	if _, err := fixture.capture.Seal(context.Background(), request, nil); !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a seal naming another Pod answered %v", err)
	}
}

// A2, node-local half: the seal waits for every container of the Pod, so a
// sidecar still writing after the main process exits is in the tree, and the
// tree is the directory after the last write.
func TestTheSealWaitsForEveryContainerAndCapturesTheLastWrite(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	fixture.capture.sealWait = 10 * time.Second
	writeFile(t, filepath.Join(fixture.stepDir(), "main.txt"), "the main process exited")

	// The sidecar keeps writing; it stops, and then the Pod's last container
	// terminates.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			writeFile(t, filepath.Join(fixture.stepDir(), "sidecar.txt"), "write "+string(rune('a'+i)))
			time.Sleep(25 * time.Millisecond)
		}
		writeFile(t, filepath.Join(fixture.stepDir(), "sidecar.txt"), "the last write")
		fixture.pods.stop(testPod)
	}()

	result, err := sealNow(t, fixture)
	if err != nil {
		t.Fatalf("sealing: %v", err)
	}
	<-done
	if result.Marker.State != output.StepSealed {
		t.Fatalf("the seal answered a %s marker", result.Marker.State)
	}

	// The digest is the directory as it is now, after the last write.
	final, err := fixture.daemon.CanonicalizeDirectory(context.Background(), fixture.stepDir())
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	if result.Digest != final.Digest {
		t.Fatalf("the seal captured %s and the directory after the last write is %s",
			result.Digest, final.Digest)
	}
	if class := ledger.New(fixture.dir).Classify(fixture.key().Directory()); class != ledger.Sealed {
		t.Errorf("a sealed step directory classifies %s", class)
	}
}

// A symlink swapped in for the step directory is refused by the call that
// would start the seal, never answered as "in progress" while the Pod runs.
func TestASealOfASymlinkedStepDirectoryIsRefusedBeforeItStarts(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	if err := os.RemoveAll(fixture.stepDir()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), fixture.stepDir()); err != nil {
		t.Fatal(err)
	}
	_, err := fixture.capture.Seal(context.Background(), sealRequest(), nil)
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Fatalf("a seal of a symlinked step directory answered %v, want unauthorized", err)
	}
	if len(fixture.capture.jobs) != 0 {
		t.Errorf("a refused seal left %d background jobs", len(fixture.capture.jobs))
	}
}

func TestASealOfARunningPodAnswersNotYetAndKeepsTheMarkerSealed(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	_, err := fixture.capture.Seal(context.Background(), sealRequest(), nil)
	if !errors.Is(err, output.ErrSealInProgress) {
		t.Fatalf("a seal of a running Pod answered %v, want in progress", err)
	}
	// The background seal runs out at its bound and says why; the next ask
	// starts another.
	if _, err := sealNow(t, fixture); !errors.Is(err, output.ErrUnresolved) {
		t.Fatalf("a background seal of a Pod that never stopped answered %v", err)
	}
	if class := ledger.New(fixture.dir).Classify(fixture.key().Directory()); class != ledger.Sealed {
		t.Errorf("after the fence the step directory is %s", class)
	}
	// Nobody may hold it again once sealed, and the same Pod's hold replays.
	if ack, err := fixture.capture.Hold(context.Background(), holdRequest()); err != nil ||
		ack.Marker.State != output.StepSealed {
		t.Errorf("a replayed hold after the seal answered %+v, %v", ack, err)
	}
}

// A1, node-local half: a daemon killed after the producer exited and before
// canonicalization restarts with the directory and its marker, and the seal
// finishes.
func TestASealInterruptedByARestartFinishesFromTheMarker(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "produced")
	_, _ = fixture.capture.Seal(context.Background(), sealRequest(), nil)

	fixture.restart(t)
	fixture.pods.stop(testPod)
	result, err := sealNow(t, fixture)
	if err != nil {
		t.Fatalf("the seal after a restart: %v", err)
	}
	published, err := publishNow(t, fixture, output.CapturePublishRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
		Digest: result.Digest,
	})
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if published.Ref.Digest != result.Digest || published.Ref.Scope != fixture.daemon.Namespace().Scope() {
		t.Fatalf("published %+v for a seal of %s", published.Ref, result.Digest)
	}
}

func TestPublishIsIdempotentDeduplicatesAndRefusesAChangedTree(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "produced")
	fixture.pods.stop(testPod)
	sealed, err := sealNow(t, fixture)
	if err != nil {
		t.Fatal(err)
	}
	request := output.CapturePublishRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput,
		Digest: sealed.Digest, Staged: sealed.Staged,
	}
	first, err := publishNow(t, fixture, request)
	if err != nil {
		t.Fatalf("publishing: %v", err)
	}
	if first.Deduplicated {
		t.Error("the first publish of a tree deduplicated")
	}
	again, err := publishNow(t, fixture, request)
	if err != nil || again.Ref != first.Ref || !again.Deduplicated {
		t.Fatalf("a second publish answered %+v, %v; want the same generation, deduplicated", again, err)
	}
	stat, err := fixture.capture.Stat(context.Background(), output.CaptureStatRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Digest: sealed.Digest,
	})
	if err != nil || stat.Ref != first.Ref {
		t.Fatalf("stat answered %+v, %v", stat, err)
	}

	// Remove the stage and change the tree: a publish for the recorded digest
	// must refuse rather than publish what is there now.
	if err := os.RemoveAll(filepath.Join(fixture.config.ScratchDir, "staged")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.config.ScratchDir, "staged"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "changed after the seal")
	if _, err := publishNow(t, fixture, request); !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a publish of a changed tree answered %v", err)
	}
}

func TestReleaseClearsTheMarkerOnceAndForAll(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	release := output.CaptureReleaseRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}
	for i := 0; i < 2; i++ {
		ack, err := fixture.capture.Release(context.Background(), release)
		if err != nil || ack.Kind != output.ReleaseAcknowledged {
			t.Fatalf("release %d answered %+v, %v", i, ack, err)
		}
	}
	if class := ledger.New(fixture.dir).Classify(fixture.key().Directory()); class != ledger.Unmanaged {
		t.Errorf("a released step directory is %s", class)
	}
	if fixture.ledger.gateOpen(t, testExecution, SourceHoldGate) {
		t.Error("the release left the cleanup gate open")
	}
	// A capture this node never held releases as a no-op.
	never := release
	never.Execution = executioncontrol.Identity{ExecutionID: "44444444-4444-4444-8444-444444444444", Fence: 1}
	if _, err := fixture.capture.Release(context.Background(), never); err != nil {
		t.Errorf("releasing a capture this node never held: %v", err)
	}
}

// An unreadable marker is never read as absent: the ledger refuses and the
// classifier every destructive path consults answers unavailable.
func TestATornMarkerRefusesEverythingAndNeverReadsAsUnmanaged(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	name, _ := markerName(fixture.key())
	markerPath := filepath.Join(fixture.store.Path(), name)
	raw, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}

	classifier := ledger.New(fixture.dir)
	for _, path := range []string{fixture.key().Directory(), "some-other-handle", ""} {
		if class := classifier.Classify(path); class != ledger.Unavailable {
			t.Errorf("with a torn marker %q classifies %s, want unavailable", path, class)
		}
	}
	fixture.pods.stop(testPod)
	if _, err := fixture.capture.Seal(context.Background(), sealRequest(), nil); !errors.Is(err, output.ErrInfrastructure) {
		t.Errorf("a seal over a torn marker answered %v", err)
	}
	if _, err := fixture.capture.Hold(context.Background(), holdRequest()); !errors.Is(err, output.ErrInfrastructure) {
		t.Errorf("a hold over a torn marker answered %v", err)
	}
}

// gateOpen reads the base record directly: whether an extension gate is open
// is a fact about the file, not about a response.
func (ledger *ExecutionLedger) gateOpen(t *testing.T, id executioncontrol.ExecutionID, gate string) bool {
	t.Helper()
	record, found, err := ledger.load(id)
	if err != nil || !found {
		t.Fatalf("reading execution %s: found=%v err=%v", id, found, err)
	}
	for _, open := range record.OpenGates {
		if open == gate {
			return true
		}
	}

	return false
}

// The sweeper's classifier restates the step-directory derivation rather than
// importing it (a reader must not import its writer). This pins the two.
func TestTheClassifierAndTheCaptureAgreeOnTheStepDirectory(t *testing.T) {
	key := output.CaptureKey{ExecutionID: testExecution, Output: testOutput}
	if got := ledger.StepDirectory(string(key.ExecutionID), string(key.Output)); got != key.Directory() {
		t.Fatalf("the classifier keys on %q and the capture writes %q", got, key.Directory())
	}
}

// A cancellation that wins the race with the control init: the capture is
// released before any hold. The release leaves a tombstone, the late hold is
// refused (the producer must not start), the step classifies unmanaged, and
// the tombstone is swept once its directory is gone and it is old enough.
func TestAReleaseBeforeTheHoldLeavesATombstoneTheHoldIsRefusedBy(t *testing.T) {
	fixture := newCaptureLedger(t)
	admitted(t, &fixture.ledgerFixture)

	release := output.CaptureReleaseRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}
	if _, err := fixture.capture.Release(context.Background(), release); err != nil {
		t.Fatalf("releasing a capture nothing held: %v", err)
	}
	if _, err := fixture.capture.Hold(context.Background(), holdRequest()); !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a hold after the release answered %v, want a conflict", err)
	}
	if _, err := os.Stat(fixture.stepDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused hold created the step directory: %v", err)
	}
	if class := ledger.New(fixture.dir).Classify(fixture.key().Directory()); class != ledger.Unmanaged {
		t.Errorf("a released step classifies %s, want unmanaged", class)
	}
	if _, err := fixture.capture.Seal(context.Background(), sealRequest(), nil); !errors.Is(err, output.ErrNotFound) {
		t.Errorf("a seal of a released step answered %v", err)
	}
	// Replayed: still released, still refusing.
	if _, err := fixture.capture.Release(context.Background(), release); err != nil {
		t.Fatalf("a replayed release: %v", err)
	}

	tombstone, err := markerName(fixture.key())
	if err != nil {
		t.Fatal(err)
	}
	fixture.capture.sweepTombstones()
	if _, found, _ := fixture.capture.load(fixture.key()); !found {
		t.Fatalf("a fresh tombstone was swept inside its retention")
	}
	fixture.capture.tombstoneRetention = time.Nanosecond
	fixture.capture.sweepTombstones()
	if _, found, _ := fixture.capture.load(fixture.key()); found {
		t.Errorf("tombstone %s outlived its retention with no directory", tombstone)
	}
}

// A held, captured step released after its seal keeps its tombstone while the
// directory exists; the artifact daemon's sweeper takes the directory first.
func TestATombstoneIsSweptOnlyAfterItsDirectory(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	release := output.CaptureReleaseRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}
	if _, err := fixture.capture.Release(context.Background(), release); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	if class := ledger.New(fixture.dir).Classify(fixture.key().Directory()); class != ledger.Unmanaged {
		t.Errorf("a released step classifies %s, want unmanaged", class)
	}
	fixture.capture.tombstoneRetention = time.Nanosecond
	fixture.capture.sweepTombstones()
	if _, found, _ := fixture.capture.load(fixture.key()); !found {
		t.Fatalf("the tombstone was swept while its directory still exists")
	}
	if err := os.RemoveAll(fixture.stepDir()); err != nil {
		t.Fatal(err)
	}
	fixture.capture.sweepTombstones()
	if _, found, _ := fixture.capture.load(fixture.key()); found {
		t.Errorf("the tombstone survived its directory")
	}
}

// Release cancels a seal still waiting for its Pod, and a job that is no
// longer registered stages nothing: no archive survives in scratch.
func TestAReleaseDuringTheTerminationWaitLeavesNoStagedArchive(t *testing.T) {
	fixture := newCaptureLedger(t)
	fixture.capture.sealWait = time.Minute
	held(t, fixture)
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "produced")

	if _, err := fixture.capture.Seal(context.Background(), sealRequest(), nil); !errors.Is(err, output.ErrSealInProgress) {
		t.Fatalf("starting the seal answered %v", err)
	}
	fixture.capture.mu.Lock()
	job := fixture.capture.jobs[fixture.key()]
	fixture.capture.mu.Unlock()
	if job == nil {
		t.Fatal("no background seal is registered")
	}

	release := output.CaptureReleaseRequest{ProtocolVersion: output.ProtocolVersion, Execution: identity(1), Output: testOutput}
	if _, err := fixture.capture.Release(context.Background(), release); err != nil {
		t.Fatalf("releasing: %v", err)
	}
	// The Pod stops after the release: a job that ignored the cancellation
	// would now canonicalize and stage.
	fixture.pods.stop(testPod)
	select {
	case <-job.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the released seal never ended")
	}
	if job.err == nil {
		t.Errorf("a released seal answered success")
	}
	time.Sleep(200 * time.Millisecond)
	staged, err := filepath.Glob(filepath.Join(fixture.config.ScratchDir, "staged", "*.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Errorf("a released seal staged %v", staged)
	}
}

// A seal that finished canonicalizing after its release was already decided
// is not staged either: the rename happens only while the job is registered.
func TestASealForgottenBeforeItFinishesRemovesItsArchive(t *testing.T) {
	fixture := newCaptureLedger(t)
	held(t, fixture)
	writeFile(t, filepath.Join(fixture.stepDir(), "result.txt"), "produced")

	gate := make(chan struct{})
	slot := func(context.Context) (func(), error) {
		<-gate
		return func() {}, nil
	}
	fixture.pods.stop(testPod)
	if _, err := fixture.capture.Seal(context.Background(), sealRequest(), slot); !errors.Is(err, output.ErrSealInProgress) {
		t.Fatalf("starting the seal answered %v", err)
	}
	fixture.capture.mu.Lock()
	job := fixture.capture.jobs[fixture.key()]
	// Forgotten without its cancellation reaching the canonicalization: the
	// job runs to the end and must find itself unregistered.
	delete(fixture.capture.jobs, fixture.key())
	fixture.capture.mu.Unlock()
	close(gate)
	<-job.done

	staged, err := filepath.Glob(filepath.Join(fixture.config.ScratchDir, "staged", "*.tar"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 || job.err == nil {
		t.Errorf("an unregistered seal staged %v (err %v)", staged, job.err)
	}
}
