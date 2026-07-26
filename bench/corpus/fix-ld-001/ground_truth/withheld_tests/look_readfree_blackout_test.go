// WITHHELD GRADING TEST — never expose this to the agent under test.
//
// Verbatim (modulo file placement) from the terminal commit 7d48580, where it was
// appended to internal/eos/look_test.go. It is delivered here as its own file in the
// same external test package so a harness can drop it into internal/eos/ at pre_state
// without colliding with whatever tests the agent writes. Do NOT copy it on top of the
// terminal SHA — look_test.go there already defines this function.
package eos_test

import (
	"context"
	"strings"
	"testing"

	"github.com/tdmtrader/lightingdesign/internal/eos"
	"github.com/tdmtrader/lightingdesign/internal/osctest"
)

// SetLook must black out WITHOUT first reading the live set: on real Eos the Select
// Active feedback is intermittently empty even when channels are live, and an empty
// read made the old code silently skip the blackout (the bug the Nomad smoke found).
// So it zeros a wide channel range deterministically (a write, no read-back), before
// setting the target.
func TestSetLookBlackoutIsReadFree(t *testing.T) {
	fake := osctest.Start(t)
	c, err := eos.Dial(context.Background(), fake.Host(), fake.Port())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Target "7" with nothing live: the old read-then-zero code would read an empty
	// active set and send NO blackout. The fix always sends the wide sweep.
	if _, _, err := c.SetLook(context.Background(), "7", 50, nil, nil); err != nil {
		t.Fatalf("SetLook: %v", err)
	}
	blackoutIdx, targetIdx := -1, -1
	for i, m := range fake.Received() {
		if m.Address == "/eos/newcmd" && len(m.Arguments) > 0 {
			if s, _ := m.Arguments[0].(string); strings.HasPrefix(s, "Chan 1 Thru ") && strings.HasSuffix(s, " At 0#") {
				blackoutIdx = i
			}
		}
		if m.Address == "/eos/chan/7" {
			targetIdx = i
		}
	}
	if blackoutIdx < 0 {
		t.Fatal("expected a read-free wide-range blackout (Chan 1 Thru N At 0#); none sent")
	}
	if targetIdx < 0 || blackoutIdx > targetIdx {
		t.Fatalf("blackout (idx %d) must come before the target set (idx %d)", blackoutIdx, targetIdx)
	}
}
