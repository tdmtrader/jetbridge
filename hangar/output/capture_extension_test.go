package output

import (
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
)

// TestTheCaptureExtensionCannotForkTheBaseExecution is the behavioural half of
// what execution_inventory_test.go asserts structurally: even with the right
// field types, a capture that names a different execution or a different epoch
// than the envelope it extends must be refused.
func TestTheCaptureExtensionCannotForkTheBaseExecution(t *testing.T) {
	identity := executioncontrol.Identity{
		ExecutionID: "0f8a5b1c-3d2e-4a6f-9b70-1c2d3e4f5a6b",
		Fence:       7,
	}
	other := executioncontrol.Identity{
		ExecutionID: "1a2b3c4d-5e6f-4708-9a1b-2c3d4e5f6071",
		Fence:       7,
	}
	envelope := executioncontrol.Envelope{
		ProtocolVersion: executioncontrol.ProtocolVersion,
		Identity:        identity,
		ActivationEpoch: 3,
		NodeUID:         "node-9f2b1d4c",
		Capability:      "control-capability-opaque-token",
	}
	admission := CaptureAdmission{
		ProtocolVersion: ProtocolVersion,
		Execution:       identity,
		ActivationEpoch: 3,
		HandoffID:       "6b1e9d40-2a77-4c11-8f3e-5d0a9c8b7e62",
		SourceHoldID:    "a4d2c8f1-9e03-4b55-86ad-71f0c3e29b48",
		Output:          "built-image",
		CaptureDeadline: NewTimestamp(mustParse(t, "2026-09-09T21:47:03Z")),
	}

	t.Run("a base execution with no capture is representable", func(t *testing.T) {
		if err := (ControlledExecution{Envelope: envelope}).Validate(); err != nil {
			t.Fatalf("an execution that opted into nothing must still be valid: %v", err)
		}
	})

	t.Run("a capture on the same identity and epoch is accepted", func(t *testing.T) {
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: admission},
		}
		if err := execution.Validate(); err != nil {
			t.Fatalf("expected the extension to be accepted: %v", err)
		}
		if !execution.HasDurableOutputCapture() {
			t.Error("HasDurableOutputCapture reported false for an execution carrying one")
		}
	})

	t.Run("a capture naming a different execution is refused", func(t *testing.T) {
		forked := admission
		forked.Execution = other
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: forked},
		}
		if err := execution.Validate(); err == nil {
			t.Fatal("a capture extension naming a different exact execution was accepted; that " +
				"is a second execution wearing the first one's envelope")
		}
	})

	t.Run("a capture naming a different fence is refused", func(t *testing.T) {
		forked := admission
		forked.Execution.Fence = identity.Fence + 1
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: forked},
		}
		if err := execution.Validate(); err == nil {
			t.Fatal("a capture extension under a different fence was accepted")
		}
	})

	t.Run("a capture naming a different activation epoch is refused", func(t *testing.T) {
		forked := admission
		forked.ActivationEpoch = envelope.ActivationEpoch + 1
		execution := ControlledExecution{
			Envelope: envelope,
			Capture:  &DurableOutputCapture{Admission: forked},
		}
		if err := execution.Validate(); err == nil {
			t.Fatal("a capture extension under a different activation epoch was accepted; one " +
				"epoch attests both facets")
		}
	})
}

func mustParse(t *testing.T, text string) time.Time {
	t.Helper()

	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatalf("parsing %q: %v", text, err)
	}

	return parsed
}
