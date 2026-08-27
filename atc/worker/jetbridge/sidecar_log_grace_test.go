package jetbridge

import (
	"sync"
	"testing"
	"time"
)

// A sidecar outlives the main container by design, and its Follow:true log
// stream does not end until the sidecar does. Joining on those goroutines
// without a bound means Wait() never returns and the step hangs.
//
// This guards the bound itself. An earlier version of streamLogs joined
// unbounded, and the scenario named for this case
// ("A step whose sidecar is still running still finishes") could not catch it:
// the fake clientset's GetLogs returns a finite stream, so the sidecar
// goroutine ended immediately and the hang never occurred under test.
func TestWaitWithGraceGivesUpOnAStreamThatNeverEnds(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		defer wg.Done()
		<-stop // stands in for a sidecar still running
	}()

	start := time.Now()
	finished := waitWithGrace(&wg, 100*time.Millisecond)
	elapsed := time.Since(start)

	if finished {
		t.Fatal("expected the wait to give up on a sidecar that is still running")
	}
	if elapsed > time.Second {
		t.Fatalf("expected to give up promptly, waited %s — a step with a "+
			"long-running sidecar would hang for as long as the sidecar runs", elapsed)
	}
}

// The other direction: output already in flight must still be captured, which
// is the defect the bound must not reintroduce.
func TestWaitWithGraceWaitsForStreamsThatDoFinish(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(50 * time.Millisecond) // a stream flushing its last lines
	}()

	if !waitWithGrace(&wg, 5*time.Second) {
		t.Fatal("expected the wait to capture a stream that finishes; " +
			"sidecar output would be cut off mid-flush")
	}
}
