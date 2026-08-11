package runtime

import (
	"errors"
	"testing"
)

func TestInterruptionErrorPreservesCauseAndIsRetryable(t *testing.T) {
	cause := errors.New("pod disappeared")
	err := NewInterruptionError(InterruptionNodeLost, cause)

	if !errors.Is(err, cause) {
		t.Fatalf("error does not preserve cause: %v", err)
	}
	if err.InterruptionReason() != InterruptionNodeLost {
		t.Fatalf("reason = %q, want %q", err.InterruptionReason(), InterruptionNodeLost)
	}
	if !err.IsRetryable() {
		t.Fatal("interruption error is not retryable")
	}
}
