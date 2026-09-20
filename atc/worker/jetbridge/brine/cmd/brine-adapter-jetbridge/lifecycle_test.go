package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

// These cases drive the adapter's OWN exit plumbing, so the resources they
// register are fakes that record having been disposed. That is the subject
// here, not a substitute for one: what is under test is whether this process
// disposes what it holds before it leaves, and a real postmaster or envtest
// control plane would answer that question no better than a boolean while
// taking ten seconds and a cluster to do it. The resources the plumbing
// actually carries are covered by the linkage case at the bottom, which
// asserts over the real registry.

func resetLifecycle(t *testing.T) {
	t.Helper()
	lifecycleMu.Lock()
	liveResources, disposedAll = nil, false
	lifecycleMu.Unlock()
	steps.TakeDisposalFailures()
	steps.DrainTrackedDisposers()
	uninstallResourceDrain()
	t.Cleanup(func() {
		lifecycleMu.Lock()
		liveResources, disposedAll = nil, false
		lifecycleMu.Unlock()
		steps.TakeDisposalFailures()
		steps.DrainTrackedDisposers()
		// The drain no longer deregisters itself when a signal arrives -- that
		// is the fix -- so a case that installed one leaves it handling
		// signals for the rest of the binary unless it is taken down here.
		uninstallResourceDrain()
	})
}

func uninstallResourceDrain() {
	drainMu.Lock()
	defer drainMu.Unlock()
	if drainSignals == nil {
		return
	}
	signal.Stop(drainSignals)
	close(drainSignals)
	drainSignals = nil
}

// The chain a cancelling signal starts stops daemons and deletes namespaces,
// and waits up to 20 seconds on one of them. The handler used to deregister
// itself before beginning it, which put SIGINT, SIGHUP and SIGTERM back to
// their default disposition -- death -- for the whole length of it. A user
// pressing ^C a second time, or a CI step escalating, killed the process with
// the daemons still up and their storage roots still on disk.
//
// The repeats are sent from INSIDE the chain, which is the only way to be sure
// they are repeats: a second SIGINT sent while the first is still pending is
// coalesced by the kernel and would prove nothing. If the handler is ever
// deregistered again, this case does not report a failure -- it takes the test
// binary down with it, which is precisely the defect.
func TestASecondSignalDuringDisposalDoesNotKillTheProcess(t *testing.T) {
	resetLifecycle(t)
	codes := captureExit(t)
	sentinel := sweepSentinel(t)

	rec := &brine.Recorder{}
	var runs atomic.Int32
	var beforeSweep atomic.Bool
	repeated := make(chan error, 1)
	steps.TrackDisposer(rec, "a daemon that takes its time stopping", func() error {
		runs.Add(1)
		var failure error
		for i := 0; i < 2; i++ {
			if failure = syscall.Kill(os.Getpid(), syscall.SIGINT); failure != nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		repeated <- failure
		_, err := os.Stat(sentinel)
		beforeSweep.Store(err == nil)
		return nil
	})

	installResourceDrain()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-repeated:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the first SIGINT never reached the resource drain")
	}
	select {
	case code := <-codes:
		if code != 130 {
			t.Errorf("the run exited %d, want 128+SIGINT", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the repeated signals stopped the disposal chain")
	}
	if runs.Load() != 1 {
		t.Errorf("the scenario disposer ran %d times, want once", runs.Load())
	}
	if !beforeSweep.Load() {
		t.Error("the sweep removed the daemon root before the daemon was stopped")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("the temp root was not swept after the repeated signals: %v", err)
	}
	if steps.TrackedDisposerCount() != 0 {
		t.Errorf("%d scenario disposers survived the drain", steps.TrackedDisposerCount())
	}
}

// captureExit substitutes the process exit so a whole exit path -- the panic
// recovery, the signal handler -- can run to its end inside the test binary.
// The substitute returns where os.Exit would not, so it releases the exit lock
// exitAfterSweep took on its way in.
func captureExit(t *testing.T) <-chan int {
	t.Helper()
	codes := make(chan int, 4)
	previous := exitProcess
	exitProcess = func(code int) {
		codes <- code
		exitMu.Unlock()
	}
	t.Cleanup(func() { exitProcess = previous })
	return codes
}

func TestNamespaceSweepFailureFailsExitBeforeTempRootSweep(t *testing.T) {
	resetLifecycle(t)
	sentinel := sweepSentinel(t)
	disposed := false
	steps.TrackDisposer(nil, "owned namespace", func() error {
		disposed = true
		return nil
	})
	previous := sweepLiveNamespaces
	t.Cleanup(func() { sweepLiveNamespaces = previous })
	swept := false
	sweepLiveNamespaces = func() {
		swept = true
		if !disposed {
			t.Error("namespace sweep ran before the tracked disposer")
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Errorf("temp roots disappeared before namespace sweep: %v", err)
		}
		steps.RecordDisposalFailure("owned namespace", errors.New("survived namespace sweep"))
	}
	var stderr bytes.Buffer
	// Check the shared exit phase directly: the sentinel itself is a temp
	// leak, so checking after the temp sweep would mask a lost disposal failure.
	if code := disposeAndCode(0, &stderr); code != 1 || !swept {
		t.Fatalf("namespace survivor did not fail exit: code=%d swept=%t", code, swept)
	}
	if !strings.Contains(stderr.String(), "disposal failed: owned namespace: survived namespace sweep") {
		t.Fatalf("namespace disposal failure was not reported: %q", stderr.String())
	}
}

// sweepSentinel makes a directory the temp sweep is obliged to remove: this
// adapter's own daemon-root shape, carrying this process's pid.
//
// It is how "the disposer ran BEFORE the sweep" is asserted rather than
// assumed. A daemon started with Setpgid takes no group signal and keeps
// serving the storage root under this directory; a sweep that runs first
// deletes the bytes out from under a live process, which is the shape of the
// defect and not a tidiness preference.
func sweepSentinel(t *testing.T) string {
	t.Helper()
	path := filepath.Join(os.TempDir(),
		fmt.Sprintf("brine-adapter-daemon-%d-sweep-sentinel", os.Getpid()))
	if err := os.MkdirAll(filepath.Join(path, "steps"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return path
}

// heldDaemon registers a scenario disposer the way a daemon fixture does --
// on the Recorder, not on the resource plane -- and reports whether the
// sentinel was still on disk when it ran.
func heldDaemon(t *testing.T, sentinel string, failure error) (*brine.Recorder, *atomic.Int32, *atomic.Bool) {
	t.Helper()
	rec := &brine.Recorder{}
	var runs atomic.Int32
	var beforeSweep atomic.Bool
	steps.TrackDisposer(rec, "the real artifact daemon", func() error {
		runs.Add(1)
		_, err := os.Stat(sentinel)
		beforeSweep.Store(err == nil)
		return failure
	})
	return rec, &runs, &beforeSweep
}

// A panic that reaches the top of the process used to take the resource state
// with it and nothing else: every daemon, pod and forwarder a scenario had
// registered on its Recorder stayed up, and then the sweep removed the root
// they were serving from.
func TestAPanicDrainsScenarioDisposersBeforeTheSweep(t *testing.T) {
	resetLifecycle(t)
	codes := captureExit(t)
	sentinel := sweepSentinel(t)
	_, runs, beforeSweep := heldDaemon(t, sentinel, nil)

	func() {
		defer exitAfterPanic()
		panic("a step body exploded")
	}()

	select {
	case code := <-codes:
		if code != 1 {
			t.Errorf("a panic exited %d, want 1", code)
		}
	default:
		t.Fatal("the panic path did not exit")
	}
	if runs.Load() != 1 {
		t.Errorf("the scenario disposer ran %d times, want once", runs.Load())
	}
	if !beforeSweep.Load() {
		t.Error("the sweep removed the daemon root before the daemon was stopped")
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("the sweep did not run: %v", err)
	}
}

// SIGINT and SIGHUP had no handler at all, so the default disposition killed
// this process with every recorder-registered fixture still running. The
// handler is driven here with a real signal: a test that called the drain
// function directly would assert nothing about whether the signal reaches it.
func TestSigintDrainsScenarioDisposersBeforeTheSweep(t *testing.T) {
	resetLifecycle(t)
	codes := captureExit(t)
	sentinel := sweepSentinel(t)
	_, runs, beforeSweep := heldDaemon(t, sentinel, nil)

	installResourceDrain()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	select {
	case code := <-codes:
		if code != 130 {
			t.Errorf("SIGINT exited %d, want 128+SIGINT", code)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("SIGINT never reached the resource drain")
	}
	if runs.Load() != 1 {
		t.Errorf("the scenario disposer ran %d times, want once", runs.Load())
	}
	if !beforeSweep.Load() {
		t.Error("the sweep removed the daemon root before the daemon was stopped")
	}
	if steps.TrackedDisposerCount() != 0 {
		t.Errorf("%d scenario disposers survived the drain", steps.TrackedDisposerCount())
	}
}

// SIGTERM was the library's alone: its drainAndExit emits the drain pair and
// exits 143, and does NOT report a disposal failure or sweep the temp root. It
// still owns the contract; what it no longer owns is the order.
func TestSigtermDrainsAndSweepsBeforeHandingOverToTheCancellationDrain(t *testing.T) {
	resetLifecycle(t)
	codes := captureExit(t)
	sentinel := sweepSentinel(t)
	_, runs, beforeSweep := heldDaemon(t, sentinel, nil)

	handed := make(chan bool, 1)
	previous := handOverToBrine
	handOverToBrine = func() error {
		_, err := os.Stat(sentinel)
		handed <- os.IsNotExist(err)
		// Reporting success without exiting is what the library does when it
		// has already drained; the grace wait below is what covers it.
		return nil
	}
	t.Cleanup(func() { handOverToBrine = previous })

	installResourceDrain()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case swept := <-handed:
		if !swept {
			t.Error("the cancellation drain was handed a process that had not swept its temp root")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("SIGTERM never reached the resource drain")
	}
	select {
	case code := <-codes:
		if code != cancelledExitCode {
			t.Errorf("a cancelled run exited %d, want %d", code, cancelledExitCode)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the handover never ended the process")
	}
	if runs.Load() != 1 {
		t.Errorf("the scenario disposer ran %d times, want once", runs.Load())
	}
	if !beforeSweep.Load() {
		t.Error("the sweep removed the daemon root before the daemon was stopped")
	}
}

// "daemon did not exit after kill" is a live process, and every one of these
// sites used to discard it with `_ =`.
func TestAFailedScenarioDisposalFailsTheRunAndNamesTheResource(t *testing.T) {
	resetLifecycle(t)
	sentinel := sweepSentinel(t)
	_, runs, _ := heldDaemon(t, sentinel, errors.New("daemon did not exit after kill"))

	var stderr bytes.Buffer
	code := disposeAndCode(0, &stderr)

	if code == 0 {
		t.Error("a scenario fixture that would not release exited 0")
	}
	if runs.Load() != 1 {
		t.Errorf("the scenario disposer ran %d times, want once", runs.Load())
	}
	report := stderr.String()
	if !strings.Contains(report, "the real artifact daemon") ||
		!strings.Contains(report, "daemon did not exit after kill") {
		t.Errorf("the report names neither the resource nor the reason: %q", report)
	}
}

// The recorder still owns the ordinary path. Whichever drain gets there first
// releases; the other must not release again -- a second stop() on a pid that
// has been reused is not a no-op.
func TestAScenarioDisposerIsNotReleasedTwice(t *testing.T) {
	resetLifecycle(t)
	sentinel := sweepSentinel(t)
	rec, runs, _ := heldDaemon(t, sentinel, nil)

	for _, disposer := range rec.DrainDisposers() {
		disposer()
	}
	var stderr bytes.Buffer
	if code := disposeAndCode(0, &stderr); code != 0 {
		t.Errorf("a clean release exited %d: %s", code, stderr.String())
	}
	if runs.Load() != 1 {
		t.Errorf("the disposer ran %d times, want once", runs.Load())
	}
}

// liveFakeResources acquires two suite-scoped fakes through the same wrapper
// buildAppResources applies, and returns the log each disposer appends to.
func liveFakeResources(t *testing.T, outerErr, innerErr error) *[]string {
	t.Helper()
	var disposed []string
	definitions := []brine.ResourceDefinition{
		{
			Name:    "outer",
			Scope:   brine.ScopeSuite,
			Factory: func(map[string]any) (any, error) { return "outer-value", nil },
			Disposer: recordingDisposer("outer", func(any) error {
				disposed = append(disposed, "outer")
				return outerErr
			}),
		},
		{
			Name:      "inner",
			Scope:     brine.ScopeSuite,
			DependsOn: []string{"outer"},
			Factory:   func(map[string]any) (any, error) { return "inner-value", nil },
			Disposer: recordingDisposer("inner", func(any) error {
				disposed = append(disposed, "inner")
				return innerErr
			}),
		},
	}
	registry, err := brine.NewResourceRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	state := trackResources(brine.NewResourceState(registry))
	if err := state.RequireAllForScope(brine.ScopeSuite); err != nil {
		t.Fatal(err)
	}
	if len(state.LiveNames()) != 2 {
		t.Fatalf("fixture is not live: %v", state.LiveNames())
	}
	return &disposed
}

// An early return from RunPlan -- an emitter write error, which is what an
// EPIPE from a dead brine CLI arrives as -- reaches the adapter as
// fail(2, "run: ...") and exits. Before this, the suite-scoped postmaster and
// envtest control plane were still running when it did.
func TestEarlyExitDisposesEveryLiveResource(t *testing.T) {
	resetLifecycle(t)
	disposed := liveFakeResources(t, nil, nil)

	var stderr bytes.Buffer
	if code := disposeAndCode(2, &stderr); code != 2 {
		t.Errorf("a clean disposal changed the exit code to %d", code)
	}

	// Reverse acquisition order: the dependent goes first, so nothing is
	// disposed while something that needs it is still running.
	if strings.Join(*disposed, ",") != "inner,outer" {
		t.Errorf("disposed %v, want inner then outer", *disposed)
	}
	if stderr.Len() != 0 {
		t.Errorf("a clean disposal said something: %s", stderr.String())
	}
}

func TestDisposalIsNotRepeatedOnASecondExitAttempt(t *testing.T) {
	resetLifecycle(t)
	disposed := liveFakeResources(t, nil, nil)

	var stderr bytes.Buffer
	disposeAndCode(0, &stderr)
	disposeAndCode(0, &stderr)

	if len(*disposed) != 2 {
		t.Errorf("disposed %v, want each resource exactly once", *disposed)
	}
}

// "daemon did not exit after kill" is a live process, not a warning. The
// library collects that message into a report its caller discards, so the run
// exited 0 with the daemon still up.
func TestAFailedDisposalFailsTheRunAndNamesTheResource(t *testing.T) {
	resetLifecycle(t)
	disposed := liveFakeResources(t, errors.New("daemon did not exit after kill"), nil)

	var stderr bytes.Buffer
	code := disposeAndCode(0, &stderr)

	if code == 0 {
		t.Error("a failed disposal exited 0")
	}
	if len(*disposed) != 2 {
		t.Errorf("a failing disposer stopped the drain: %v", *disposed)
	}
	report := stderr.String()
	if !strings.Contains(report, "outer") || !strings.Contains(report, "daemon did not exit after kill") {
		t.Errorf("the report names neither the resource nor the reason: %q", report)
	}
}

// A panicking disposer is swallowed by the drain's recover() exactly as a
// returned error is swallowed by the discarded report, so it must be recorded
// on the way past -- and re-raised, because the library's isolation is what
// keeps the rest of the drain running.
func TestAPanickingDisposalIsRecordedAndStillIsolated(t *testing.T) {
	resetLifecycle(t)
	var disposed []string
	definitions := []brine.ResourceDefinition{{
		Name:    "exploder",
		Scope:   brine.ScopeSuite,
		Factory: func(map[string]any) (any, error) { return nil, nil },
		Disposer: recordingDisposer("exploder", func(any) error {
			disposed = append(disposed, "exploder")
			panic("stop(): signal: killed")
		}),
	}}
	registry, err := brine.NewResourceRegistry(definitions)
	if err != nil {
		t.Fatal(err)
	}
	state := trackResources(brine.NewResourceState(registry))
	if err := state.RequireAllForScope(brine.ScopeSuite); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	code := disposeAndCode(0, &stderr)

	if code == 0 || len(disposed) != 1 {
		t.Errorf("code=%d disposed=%v", code, disposed)
	}
	if !strings.Contains(stderr.String(), "signal: killed") {
		t.Errorf("the panic's reason was lost: %q", stderr.String())
	}
}

// The plumbing above is only worth anything if the real resource plan goes
// through it: every declared disposer must reach the registry wrapped.
func TestEveryDeclaredDisposerIsWrapped(t *testing.T) {
	declared := steps.ResourceDefinitions()
	registry, err := buildAppResources()
	if err != nil {
		t.Fatal(err)
	}
	built := registry.Definitions()
	if len(built) != len(declared) {
		t.Fatalf("built %d definitions from %d declared", len(built), len(declared))
	}
	for i, definition := range declared {
		if built[i].Name != definition.Name {
			t.Fatalf("definition %d is %q, want %q", i, built[i].Name, definition.Name)
		}
		if definition.Disposer == nil {
			if built[i].Disposer != nil {
				t.Errorf("%q gained a disposer it never declared", definition.Name)
			}
			continue
		}
		if built[i].Disposer == nil {
			t.Errorf("%q lost its disposer", definition.Name)
			continue
		}
		if reflect.ValueOf(built[i].Disposer).Pointer() == reflect.ValueOf(definition.Disposer).Pointer() {
			t.Errorf("%q reached the registry unwrapped: its failure would be discarded", definition.Name)
		}
	}
}
