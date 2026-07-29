package runner

import (
	"context"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/provider"
)

func TestSignalBoundaryControlOnlyStopsAtPendingBoundary(t *testing.T) {
	var mu sync.Mutex
	stops := 0
	control := newSignalBoundaryControl(func() error { mu.Lock(); stops++; mu.Unlock(); return nil })
	defer control.Close()
	boundary := provider.Boundary{SessionID: "session-1", TranscriptCursor: 4}
	if err := control.AtSafeBoundary(context.Background(), boundary); err != nil {
		t.Fatal(err)
	}
	if got := boundaryStops(&mu, &stops); got != 0 {
		t.Fatalf("stop count = %d", got)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitForPending(t, control, true)
	if err := control.AtSafeBoundary(context.Background(), boundary); err != nil {
		t.Fatal(err)
	}
	if got := boundaryStops(&mu, &stops); got != 1 {
		t.Fatalf("stop count = %d", got)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitForPending(t, control, true)
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR2); err != nil {
		t.Fatal(err)
	}
	waitForPending(t, control, false)
	if err := control.AtSafeBoundary(context.Background(), boundary); err != nil {
		t.Fatal(err)
	}
	if got := boundaryStops(&mu, &stops); got != 1 {
		t.Fatalf("cancelled request stopped = %d", got)
	}
}

func TestLegacyClaudeAdapterDoesNotAdvertiseCaptureOrResumeCapabilities(t *testing.T) {
	capabilities := (legacyClaudeAdapter{}).Capabilities()
	if capabilities.SafeBoundary || capabilities.SessionExport || capabilities.NativeResume {
		t.Fatalf("legacy Claude capabilities = %#v", capabilities)
	}
}

func waitForPending(t *testing.T, control *signalBoundaryControl, want bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		control.mu.Lock()
		got := control.pending
		control.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending = %t, want %t", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func boundaryStops(mu *sync.Mutex, stops *int) int { mu.Lock(); defer mu.Unlock(); return *stops }
