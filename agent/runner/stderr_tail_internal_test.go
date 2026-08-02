package runner

import (
	"strings"
	"testing"
)

// This test lives in the internal (package runner) test file, not
// runner_test.go, because boundedTailWriter is unexported and runner_test.go
// is package runner_test (external, black-box tests of Run()). The
// black-box test TestRunCapturedClaudeStderrTailIsBounded in runner_test.go
// covers the same property behaviorally (Run() stays fast against megabytes
// of stderr); this test pins the exact invariant precisely, which requires
// access to the writer's internal buffer.

// TestBoundedTailWriterNeverGrowsPastTwiceItsCap confirms the buffer that
// backs boundedTailWriter is actually bounded, not just "usually small": far
// more than cap bytes, written across many small writes (as a real
// subprocess's stderr would arrive), must never leave the internal buffer
// larger than 2x cap.
func TestBoundedTailWriterNeverGrowsPastTwiceItsCap(t *testing.T) {
	const capBytes = 64
	w := newBoundedTailWriter(capBytes)
	chunk := []byte("0123456789")
	for i := 0; i < 10_000; i++ {
		if _, err := w.Write(chunk); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if len(w.buf) > capBytes*2 {
			t.Fatalf("internal buffer grew to %d bytes (cap %d) after %d writes; not bounded", len(w.buf), capBytes, i+1)
		}
	}
}

// TestBoundedTailWriterTailReturnsOnlyTheMostRecentCapBytes confirms tail()
// itself never returns more than cap bytes, and that it is genuinely the
// *tail* (most recent content), not an arbitrary bounded slice.
func TestBoundedTailWriterTailReturnsOnlyTheMostRecentCapBytes(t *testing.T) {
	const capBytes = 16
	w := newBoundedTailWriter(capBytes)
	if _, err := w.Write([]byte(strings.Repeat("a", 100))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("END-OF-STREAM")); err != nil {
		t.Fatal(err)
	}
	got := w.tail()
	if len(got) > capBytes {
		t.Fatalf("tail() returned %d bytes, want <= %d", len(got), capBytes)
	}
	if !strings.HasSuffix(got, "STREAM") {
		t.Fatalf("tail() = %q, does not look like the most recently written bytes", got)
	}
}
