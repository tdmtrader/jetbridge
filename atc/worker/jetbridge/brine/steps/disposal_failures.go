package steps

// Where a failed teardown goes, now that both drains throw them away.
//
// Releasing this adapter's fixtures happens on two paths, and NEITHER reports
// what went wrong. The recorder drain (the runner library's pipeline.go) wraps
// every disposer in a recover() that keeps nothing but a boolean `partial` on
// an event. The resource plane's DisposeScope collects a []DisposalFailure
// that the same file discards with `_ =`. So a daemon that refused to die --
// realdaemon.go's "daemon did not exit after kill" -- left a live process and
// its storage behind on a run that exited 0, and the sentence naming it was
// destroyed by the very recover() that was meant to isolate it.
//
// Panicking was the wrong answer to that. A panic raised inside a drained
// disposer is swallowed by the same recover(), so it loses the message just as
// completely AND skips every disposer registered before it -- one daemon that
// will not stop takes the rest of the scenario's teardown with it. Recording
// is the right answer: every disposer still runs, and the adapter's exit path
// (cmd/brine-adapter-jetbridge) prints what was collected and refuses to exit
// 0. WithDisposalVerdict also includes these failures in run_end so an engine
// consumer cannot mistake a completed event stream for a successful cleanup.

import (
	"fmt"
	"sync"
)

var (
	disposalMu       sync.Mutex
	disposalFailures []string
)

// RecordDisposalFailure notes that releasing resource failed. A nil error is a
// clean disposal and records nothing, so a disposer body can hand it the
// result of a stop() directly.
//
// Disposers run on the step goroutine during a natural drain and on the signal
// goroutine during a cancellation drain, hence the lock.
func RecordDisposalFailure(resource string, err error) {
	if err == nil {
		return
	}
	disposalMu.Lock()
	defer disposalMu.Unlock()
	disposalFailures = append(disposalFailures, fmt.Sprintf("%s: %v", resource, err))
}

// TakeDisposalFailures returns everything recorded so far and clears the
// collection, so an exit path cannot report the same failure twice.
func TakeDisposalFailures() []string {
	disposalMu.Lock()
	defer disposalMu.Unlock()
	taken := disposalFailures
	disposalFailures = nil
	return taken
}

// DisposalFailureCount observes failures without consuming exit diagnostics.
func DisposalFailureCount() int {
	disposalMu.Lock()
	defer disposalMu.Unlock()
	return len(disposalFailures)
}
