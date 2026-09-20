package main

// What this process owes the machine on its way out, and who makes it pay.
//
// The runner library disposes scoped resources INLINE inside RunPlan -- three
// `_ = p.resources.DisposeScope(...)` calls, none of them deferred. Every early
// return above them therefore exits with the suite's resources still running,
// and RunPlan returns early on any emitter write error: an EPIPE, which is
// exactly what happens when the brine CLI on the other end of the event stream
// dies. What stays behind is not a file handle. It is an envtest control plane
// -- kube-apiserver and etcd, started with Setpgid so no group signal reaches
// them -- and a postmaster. The adapter then called os.Exit, which runs no
// defers, so nothing reclaimed them either.
//
// So the adapter keeps its own handle on the live resource state and disposes
// it from the ONE function that ends this process. Ordering matters: dispose
// first, sweep the temp roots second, exit last, because a daemon still
// running holds files under the root the sweep is about to remove.
//
// What the exit path must release is NOT only the resource state. The fixtures
// that own processes -- daemons, a registrar pod, a TCP forwarder -- are
// registered on the SCENARIO RECORDER, because a scenario-scoped resource is
// acquired before every scenario in the suite and a daemon wired that way cost
// 70 seconds. brine-go exports no way to drain a recorder from outside the
// pipeline, so steps.TrackDisposer registers each of those releases a second
// time in a process-level set, and steps.DrainTrackedDisposers is the first
// thing every exit here does. It has to be first in both directions: those
// daemons are started with Setpgid, so no group signal reaches them, and the
// sweep below removes the very root they are serving from.
//
// SIGTERM: brine.InstallSigtermDrain owns the cancellation contract -- the
// recorder_drain event pair and exit 143, both pinned by TestEngineRelease and
// the hold-drain case -- but its drainAndExit (brine-go pkg/brine/
// cancellation.go) discards every disposal failure, never sweeps this
// adapter's temp root, and calls os.Exit itself. Installing a SECOND handler
// alongside it does not fix that: signal.Notify delivers to every registered
// channel, two independent goroutines cannot be ordered, and the library's
// would sometimes exit first with the sweep unrun. So the library's handler is
// not installed until it is needed. This adapter takes SIGTERM itself, runs
// the whole chain -- tracked disposers, resource state, failure report, sweep
// -- and only then installs brine's handler and re-raises the signal, which
// hands the cancellation contract its events and its exit code with nothing
// left outstanding. The cost is that brine.CancelRequested latches at the end
// of that chain rather than at its start, so the pipeline may still be inside
// a step while it runs; it was already inside one when the signal arrived.
//
// SIGINT and SIGHUP had no handler at all, and the default disposition for
// both is death.
//
// The handler stays installed for the WHOLE chain. It used to call
// signal.Stop(signals) on its way in -- which returns SIGINT, SIGHUP and
// SIGTERM to that default disposition -- and only then start disposing. The
// chain below it stops daemons, deletes namespaces and waits up to 20 seconds
// on a single one of them, and a run cancelled during that window took a
// second signal straight to the default handler: a user pressing ^C twice, a
// CI step timing out, an engine escalating. The process died with storage
// roots on disk and envtest control planes still running -- the exact
// situation this file exists to prevent, reached BY the code meant to prevent
// it. Later signals are now swallowed by the same goroutine: the first one
// latches, every repeat is reported and ignored, and nothing reaches a default
// disposition until brine's own handler has been installed over ours.

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/worker/jetbridge/brine/steps"
)

var (
	lifecycleMu sync.Mutex
	// liveResources is the resource state this process's pipeline acquires
	// against. The exit path disposes whatever is still live in it.
	liveResources *brine.ResourceState
	// disposedAll makes the process-level disposal happen once: the signal
	// goroutine and the main goroutine can both reach it.
	disposedAll bool

	// exitMu is taken and never released. exitAfterSweep ends in os.Exit, so
	// a second caller (the signal goroutine arriving while main is already
	// leaving) parks here instead of sweeping and exiting a second time.
	exitMu sync.Mutex

	// exitProcess is os.Exit, indirected so that the exit paths -- the panic
	// path and the signal handler, neither of which can be reached by calling
	// a helper -- can be driven whole by a test instead of being trusted. A
	// substitute RETURNS, where os.Exit does not, so it also releases exitMu.
	exitProcess = os.Exit

	// Indirect so exit-path tests can verify that sweep failures affect the
	// exit code before temporary roots disappear, without a live cluster.
	sweepLiveNamespaces = func() { _ = steps.SweepLiveNamespaces() }

	// drainSignals is the channel the cancelling signals are delivered to. It
	// is held so that a second installResourceDrain replaces the first rather
	// than racing it; it is never stopped on the way out, which is the whole
	// point -- see the header.
	drainMu      sync.Mutex
	drainSignals chan os.Signal
)

// trackResources hands the exit path the state it must drain, and returns it
// so the caller can pass it straight to WithResources.
func trackResources(state *brine.ResourceState) *brine.ResourceState {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	liveResources = state
	return state
}

// disposeLiveResources releases every resource still live, newest first, once.
//
// The failures are not read here: recordingDisposer (registry.go) has already
// put each one in the collector, which is also where a failure from the
// pipeline's own in-line DisposeScope calls ends up.
func disposeLiveResources() {
	lifecycleMu.Lock()
	defer lifecycleMu.Unlock()
	if liveResources == nil || disposedAll {
		return
	}
	disposedAll = true
	_ = liveResources.DisposeAll()
}

// recordingDisposer wraps a resource disposer so its failure survives.
//
// DisposeScope's []DisposalFailure report is discarded by the pipeline, and a
// disposer that panics is rendered into that same discarded report. Both are
// recorded here first. The panic is re-raised so the library's own isolation
// and report keep the shape its contract pins.
func recordingDisposer(name string, disposer brine.ResourceDisposer) brine.ResourceDisposer {
	return func(value any) (err error) {
		defer func() {
			if r := recover(); r != nil {
				steps.RecordDisposalFailure("resource "+name, fmt.Errorf("disposer panicked: %v", r))
				panic(r)
			}
			steps.RecordDisposalFailure("resource "+name, err)
		}()
		return disposer(value)
	}
}

// installResourceDrain makes a cancelling signal leave the way any other exit
// leaves: scenario disposers drained, resources disposed, temp roots swept, a
// code that says what happened. SIGINT and SIGHUP exit here; SIGTERM runs the
// same chain and then hands the signal to the library's cancellation drain.
//
// signal.Notify rather than any check of the current disposition: a CI shell
// can hand this process SIGHUP already ignored, and installing a handler over
// SIG_IGN is precisely what Notify does -- which a shell `trap` cannot.
func installResourceDrain() {
	drainMu.Lock()
	defer drainMu.Unlock()
	// Installing twice in one process is a mistake; the tests do it once per
	// case. Either way only one registration may be live, or a signal starts
	// two disposal chains that cannot be ordered against each other.
	if drainSignals != nil {
		signal.Stop(drainSignals)
		close(drainSignals)
	}
	// Room for the repeats: a second ^C, and the SIGTERM this process re-raises
	// at itself during the handover, which our channel is still registered for.
	signals := make(chan os.Signal, 8)
	drainSignals = signals
	signal.Notify(signals, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	go func() {
		draining := false
		for received := range signals {
			if draining {
				// Swallowed, not acted on. Reaching here at all is the point:
				// the handler is still installed, so the runtime delivered the
				// signal to this channel instead of killing the process in the
				// middle of the chain below.
				fmt.Fprintf(os.Stderr, "%s: %v: already disposing; ignored\n", adapterName, received)
				continue
			}
			draining = true
			// Latched BEFORE the chain: the pipeline keeps running on the main
			// goroutine while this one disposes, and without the latch its next
			// scenario boundary would Require every resource this chain just
			// disposed -- a fresh postmaster or control plane, started to be
			// orphaned by the os.Exit at the end of the chain.
			steps.MarkLeaving()
			steps.BoundLiveNamespaceSweep()
			fmt.Fprintf(os.Stderr, "%s: %v: disposing live resources\n", adapterName, received)
			if received == syscall.SIGTERM {
				handOverToCancellationDrain()
				continue
			}
			code := 1
			if number, ok := received.(syscall.Signal); ok {
				code = 128 + int(number)
			}
			exitAfterSweep(code)
		}
	}()
}

// cancelledExitCode is what a cancelled run exits with: 128 + SIGTERM. It is
// brine-go's cancelExitCode, named here because this adapter falls back to it
// when the handover cannot be completed.
const cancelledExitCode = 143

// cancellationHandoverGrace bounds the wait for brine's drain to end the
// process. It is a backstop, not a timing assumption: drainAndExit emits two
// events and calls os.Exit. The engine escalates to SIGKILL after its own
// grace (5s by default), so this stays well inside it.
const cancellationHandoverGrace = 2 * time.Second

// handOverToBrine installs brine-go's own SIGTERM handler and gives the signal
// back to it, which is the last thing this process does under its own power.
//
// Our own channel is deliberately still registered when this runs. signal.Notify
// delivers to every registered channel, so the library's handler gets the
// re-raised SIGTERM whether or not ours does; ours has already latched and
// reports it as a repeat. Stopping ours first would reopen the window this
// file's header is about, between the Stop and the library's Notify.
//
// It is a variable so that the chain which must run BEFORE it -- the tracked
// disposers, the resource state, the report, the sweep -- can be asserted on
// in this package: the real handover ends the process from inside the library,
// where a test cannot follow it, and the cases that do follow it through a
// real subprocess need an engine and a cluster.
var handOverToBrine = func() error {
	brine.InstallSigtermDrain()
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}

// handOverToCancellationDrain settles this adapter's debts and then gives
// SIGTERM back to the library that owns the cancellation contract.
//
// Everything the library's drain would skip happens first: the scenario's
// tracked disposers, the live resource state, the disposal report, and the
// sweep. By the time brine's handler runs, its recorder disposers are already
// done (each entry releases once), so it emits the drain pair it is pinned to
// emit, disposes an empty resource state, and exits 143.
func handOverToCancellationDrain() {
	// Taken and never released, exactly as exitAfterSweep takes it: whoever
	// arrives second must not sweep or exit underneath this.
	exitMu.Lock()

	disposeAndCode(cancelledExitCode, os.Stderr)
	for _, leak := range steps.SweepAdapterDaemonRoots() {
		fmt.Fprintln(os.Stderr, adapterName+": temp leak:", leak)
	}

	if err := handOverToBrine(); err != nil {
		fmt.Fprintf(os.Stderr, "%s: re-raising SIGTERM: %v\n", adapterName, err)
		exitProcess(cancelledExitCode)
		return
	}
	time.Sleep(cancellationHandoverGrace)
	fmt.Fprintf(os.Stderr, "%s: the cancellation drain did not end the process\n", adapterName)
	exitProcess(cancelledExitCode)
}

// exitAfterSweep is how this adapter leaves under its own power -- every path
// but SIGTERM, which runs the same chain and then hands over.
//
// A daemon fixture makes directories outside the tree -- a built binary, a node
// storage root -- and `os.Exit` runs no defers, so every exit path disposes and
// sweeps explicitly or the processes and the bytes stay (571 directories, 42 GB,
// were found in one user's temp directory before the sweep existed). A leak
// fails the run: nobody reads a warning on a green run.
func exitAfterSweep(code int) {
	exitMu.Lock()
	code = disposeAndCode(code, os.Stderr)
	leaks := steps.SweepAdapterDaemonRoots()
	for _, leak := range leaks {
		fmt.Fprintln(os.Stderr, adapterName+": temp leak:", leak)
	}
	if len(leaks) != 0 && code == 0 {
		code = 1
	}
	exitProcess(code)
}

// disposeAndCode disposes what is still live, reports every disposal failure
// collected during this process's life, and returns the exit code the run has
// earned. It is exitAfterSweep without the sweep and without os.Exit, so the
// rule it enforces can be asserted on.
func disposeAndCode(code int, out io.Writer) int {
	steps.BoundLiveNamespaceSweep()
	steps.MarkLeaving()
	// The recorder's disposers first, and the resource state second: a
	// daemon is started on top of a control plane and a database, and the
	// thing that stops being written to has to stop before the thing it was
	// written through goes away.
	steps.DrainTrackedDisposers()
	disposeLiveResources()
	// Both exit paths pass here before sweeping temp roots. Verify namespace
	// deletion before collecting failures so a survivor makes the run fail.
	sweepLiveNamespaces()
	for _, failure := range steps.TakeDisposalFailures() {
		fmt.Fprintln(out, adapterName+": disposal failed:", failure)
		if code == 0 {
			code = 1
		}
	}
	return code
}

// exitAfterPanic is main's last defer: a panic that reaches the top of the
// process must still release what the process started. Re-panicking would
// print the same trace and exit 2 -- the refusal's code -- so the trace is
// printed here and the exit is 1, which no protocol answer uses.
func exitAfterPanic() {
	recovered := recover()
	if recovered == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s: panic: %v\n%s\n", adapterName, recovered, debug.Stack())
	exitAfterSweep(1)
}
