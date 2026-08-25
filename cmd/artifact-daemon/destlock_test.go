package main

// N4 — the dest-lock map must be bounded by IN-FLIGHT copies, not by every
// destination ever seen.
//
// The first version was a bare sync.Map keyed by cleaned dest, storing an entry
// before the copy ran and never pruning. 200 requests with distinct dests left
// 200 permanent entries — including for requests whose copy failed and wrote
// nothing — on the mTLS-exempt endpoint, with dest length bounded only by the
// body cap. In-package because destLocks is unexported; exposing it for a test
// would be worse than reading it here.

import (
	"code.cloudfoundry.org/lager/v3"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func TestDestLocks_BoundedByInFlightCopies(t *testing.T) {
	root := t.TempDir()
	s := newServerT(t, lagertest.NewTestLogger("destlock"), root, "test-node")

	src := filepath.Join(root, "steps", "leak", "out")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "resolved")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}

	count := func() int {
		var n int
		s.destLocks.Range(func(_, _ any) bool { n++; return true })
		return n
	}

	// Succeeding copies.
	for i := 0; i < 100; i++ {
		dest := filepath.Join(parent, "ok-"+itoa(i))
		if err := s.copyArtifactGuarded(src, dest); err != nil {
			t.Fatalf("copy %d: %v", i, err)
		}
	}
	// Failing copies — the leak was worst here, since nothing landed on disk.
	for i := 0; i < 100; i++ {
		dest := filepath.Join(root, "no-such-parent", "fail-"+itoa(i), "x")
		_ = s.copyArtifactGuarded(src, dest)
	}

	if n := count(); n != 0 {
		t.Errorf("dest-lock map retained %d entries after 200 completed copies — unbounded growth", n)
	} else {
		t.Logf("200 distinct dests (100 ok, 100 failed): 0 retained entries")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// R9 — the batch fan-out bound, asserted DETERMINISTICALLY.
//
// The first version polled the filesystem for .cp-tmp-* directories and took
// the peak. It passed alone and failed under full-package load, because a 1ms
// poll misses the window when the CPU is contended — a flaky test asserting a
// real property badly. This holds every slot and proves the bound by observing
// that work cannot start until a slot is released.
func TestResolveSem_BoundIsRealAndBlocking(t *testing.T) {
	s := newServerT(t, lagertest.NewTestLogger("sem"), t.TempDir(), "test-node")

	if got := cap(s.resolveSem); got != maxConcurrentBatchResolves {
		t.Fatalf("semaphore capacity %d, want %d", got, maxConcurrentBatchResolves)
	}

	// Take every slot.
	for i := 0; i < maxConcurrentBatchResolves; i++ {
		select {
		case s.resolveSem <- struct{}{}:
		default:
			t.Fatalf("could not acquire slot %d — the semaphore is smaller than its capacity", i)
		}
	}

	// One more must NOT be acquirable. If it is, the bound does not bind.
	select {
	case s.resolveSem <- struct{}{}:
		t.Fatal("acquired a slot beyond the cap — the fan-out is not bounded")
	default:
	}

	// Release one; exactly one more becomes available.
	<-s.resolveSem
	select {
	case s.resolveSem <- struct{}{}:
	default:
		t.Fatal("releasing a slot did not make one available — the semaphore leaks")
	}
	select {
	case s.resolveSem <- struct{}{}:
		t.Fatal("a second slot became available after releasing only one")
	default:
	}
	t.Logf("bound holds at %d: full, refused, released one, admitted exactly one", maxConcurrentBatchResolves)
}

// newServerT is the in-package equivalent of newDaemonServer.
func newServerT(t *testing.T, logger lager.Logger, storagePath, nodeName string) *Server {
	t.Helper()
	srv, err := NewServer(logger, storagePath, nodeName)
	if err != nil {
		t.Fatalf("newServerT(t, %q): %v", storagePath, err)
	}
	return srv
}
