package steps

// Who releases a scenario's fixtures when the scenario never ends.
//
// A daemon, a registrar pod, a kubeconfig directory and a TCP forwarder are
// registered on the SCENARIO RECORDER, not on the resource plane, and that is
// deliberate: brine acquires every ScopeScenario resource before EVERY
// scenario, so a daemon wired as a resource is built and killed 380 times to
// be used 5 (measured: 118s -> 188s). The recorder drains LIFO at scenario end
// on pass and on failure, which covers every ORDINARY exit.
//
// It covers no other kind. The recorder drain runs inside the pipeline, and
// brine-go exports nothing that drains it from outside; the adapter's exit path
// held only the resource state. So an adapter that took SIGINT, took SIGHUP,
// or panicked left every recorder-registered daemon running -- and then
// SweepAdapterDaemonRoots removed the storage root out from under the ones
// started with Setpgid, which no group signal reaches either. What was left
// was a live artifact daemon serving a directory that no longer existed.
//
// TrackDisposer closes that by registering the SAME release twice: with the
// recorder, which still owns the ordinary path and its drain events, and with
// a process-level set the adapter's exit path drains. Whichever runs first
// marks the entry done, so the other skips it -- the release happens exactly
// once no matter which way this process leaves.
//
// And it is recorded. Every one of these sites discarded its error with `_ =`,
// which is how "daemon did not exit after kill" became a sentence nobody read
// on a run that exited 0. RecordDisposalFailure puts it where the adapter's
// exit path prints it and refuses to exit 0.

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// trackedDisposer is one release, and whether it has already happened.
type trackedDisposer struct {
	resource string
	dispose  func() error
	done     bool
}

var (
	trackedMu        sync.Mutex
	trackedDisposers []*trackedDisposer
)

// TrackDisposer registers dispose with the scenario's recorder AND with this
// process's live set, naming the resource so a failure to release it can be
// read. dispose returning nil is a clean release and says nothing.
//
// The recorder is allowed to be nil so a fixture that owns something before it
// is handed a recorder can still be released on the way out.
//
// The returned func releases the same entry once, for the fixture whose
// lifetime is one STEP rather than one scenario -- a daemon started to serve a
// single read. `defer TrackDisposer(rec, name, stop)()` registers it now and
// releases it at the end of the step, and a signal arriving in between finds
// it registered instead of finding nothing. Callers whose fixture lives for
// the scenario ignore it, which is what almost all of them do.
func TrackDisposer(rec *brine.Recorder, resource string, dispose func() error) func() {
	tracked := &trackedDisposer{resource: resource, dispose: dispose}
	trackedMu.Lock()
	trackedDisposers = append(trackedDisposers, tracked)
	trackedMu.Unlock()
	if rec != nil {
		rec.RegisterDisposer(func() { tracked.run(true) })
	}
	// Registered after the exit path has already drained: the pipeline is
	// still running on another goroutine while the process leaves. Nothing
	// will drain this entry a second time, so release it now rather than
	// leave a daemon serving from a root the sweep is about to remove.
	if Leaving() {
		tracked.run(false)
	}

	return func() { tracked.run(false) }
}

// run releases once, records what went wrong, and survives a panic.
//
// reraise is true on the recorder's own drain, whose recover() is what keeps
// the disposers under this one running -- the panic is recorded here and then
// handed back so the library's isolation keeps the shape its contract pins.
// The process drain has no such isolation above it, so it stops here.
func (t *trackedDisposer) run(reraise bool) {
	trackedMu.Lock()
	if t.done {
		trackedMu.Unlock()
		return
	}
	t.done = true
	trackedMu.Unlock()

	defer func() {
		if r := recover(); r != nil {
			RecordDisposalFailure(t.resource, fmt.Errorf("disposer panicked: %v", r))
			if reraise {
				panic(r)
			}
		}
	}()
	RecordDisposalFailure(t.resource, t.dispose())
}

// DrainTrackedDisposers releases everything the recorder drain has not already
// released, newest first, and forgets it.
//
// LIFO for the reason the recorder drains LIFO: a fixture built on another
// one's output -- a daemon under a storage root, a route onto a daemon's port
// -- must go first. The loop re-reads the set because a release may register
// another one; it ends when nothing new appeared.
//
// Calling this twice releases nothing twice: the done flag is on the entry,
// which the recorder's closure also holds.
// leaving latches once the process has decided to exit. From then on every
// resource factory refuses, a disposer tracked late runs at once, and the
// waits on the way out are the short ones: the process is not coming back
// for anything it leaves behind, and the engine escalates to SIGKILL 5s
// after SIGTERM (BRINE_ENGINE_SUBPROCESS_KILL_GRACE_SECS), which a 20s wait
// on one daemon would overrun.
var leaving atomic.Bool

// MarkLeaving latches the exit; it is set by the exit path before it drains.
func MarkLeaving() { leaving.Store(true) }

// Leaving reports whether the exit path has begun.
func Leaving() bool { return leaving.Load() }

// stopWait is how long a daemon stop waits for the group to be gone: the
// full budget during a run, a short one once the process is leaving.
func stopWait() time.Duration {
	if Leaving() {
		return 2 * time.Second
	}
	return 20 * time.Second
}

func DrainTrackedDisposers() {
	for {
		trackedMu.Lock()
		pending := trackedDisposers
		trackedDisposers = nil
		trackedMu.Unlock()
		if len(pending) == 0 {
			return
		}
		for i := len(pending) - 1; i >= 0; i-- {
			pending[i].run(false)
		}
	}
}

// TrackedDisposerCount reports how many releases are registered and not yet
// performed, so a test can say that the set is the thing being drained.
func TrackedDisposerCount() int {
	trackedMu.Lock()
	defer trackedMu.Unlock()
	pending := 0
	for _, tracked := range trackedDisposers {
		if !tracked.done {
			pending++
		}
	}
	return pending
}

// releasedIfGone is the one thing a deleting disposer is allowed to forgive.
//
// The objects these fixtures create -- a node, an EndpointSlice, a registrar
// pod -- are deleted by name at scenario end, and a scenario that already
// deleted one, or a control plane that took it away, leaves nothing to
// release. Every OTHER error is a real failure to release a real object and
// now fails the run; before TrackDisposer they were all discarded alike.
func releasedIfGone(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// releasedIfAlreadyClosed is the same forgiveness for a connection.
//
// A hijacked exchange is closed by the http server's own shutdown before the
// disposer reaches it, and closing it a second time answers net.ErrClosed. The
// socket IS released, so that is not a failure to release -- but "use of
// closed network connection" is the ONLY error allowed through here, and a
// refused close for any other reason is a real one.
func releasedIfAlreadyClosed(err error) error {
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
