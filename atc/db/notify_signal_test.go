package db_test

import (
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/db"
)

func TestNotifySignal_BasicWakeup(t *testing.T) {
	s := db.NewNotifySignal()

	s.Signal()

	select {
	case <-s.C():
		// expected
	case <-time.After(time.Second):
		t.Fatal("expected wake-up from Signal()")
	}
}

func TestNotifySignal_CoalescesMultipleSignals(t *testing.T) {
	s := db.NewNotifySignal()

	// Multiple signals before read should produce a single wake-up.
	for i := 0; i < 100; i++ {
		s.Signal()
	}

	select {
	case <-s.C():
		// expected: one wake-up
	case <-time.After(time.Second):
		t.Fatal("expected wake-up")
	}

	// Channel should be empty now — no second wake-up without another Signal.
	select {
	case <-s.C():
		t.Fatal("unexpected second wake-up")
	case <-time.After(50 * time.Millisecond):
		// expected: no second wake-up
	}
}

func TestNotifySignal_ReadThenSignalAgain(t *testing.T) {
	s := db.NewNotifySignal()

	s.Signal()
	<-s.C()

	// After consuming, a new Signal produces another wake-up.
	s.Signal()

	select {
	case <-s.C():
		// expected
	case <-time.After(time.Second):
		t.Fatal("expected wake-up after consuming + Signal()")
	}
}

func TestNotifySignal_SignalDuringProcessingIsNotLost(t *testing.T) {
	s := db.NewNotifySignal()

	// Reader gets first signal, starts processing
	s.Signal()
	<-s.C()

	// During processing, more signals arrive — channel has capacity 1,
	// so one coalesced signal is buffered.
	s.Signal()
	s.Signal()
	s.Signal()

	// The buffered signal is available immediately
	select {
	case <-s.C():
		// expected: coalesced signal from during processing
	case <-time.After(time.Second):
		t.Fatal("signal during processing was lost")
	}
}

func TestNotifySignal_ConcurrentSignals(t *testing.T) {
	s := db.NewNotifySignal()

	var wg sync.WaitGroup
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Signal()
		}()
	}
	wg.Wait()

	// At least one wake-up should be available
	select {
	case <-s.C():
		// expected
	case <-time.After(time.Second):
		t.Fatal("expected wake-up after concurrent signals")
	}
}
