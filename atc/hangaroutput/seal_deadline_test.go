package hangaroutput_test

// Req 17's seal deadline is 5 minutes, configurable from 30 seconds through 30
// minutes, and Coordinator.SealDeadline is the only place in this repository a
// value can come from: it is an exported field on an exported struct, and
// sealDeadline() substituted the default on zero and accepted any other value
// whatever. A bound that exists in a constant and is applied nowhere is not a
// bound -- ValidateSealDeadline had no caller at all, which is how the
// reachability rule found it.
//
// Against real PostgreSQL, through the production coordinator, because what
// has to be proved is that the refusal happens BEFORE the deadline is
// committed: a seal deadline written and then rejected is a capture bounded by
// a number nobody approved.

import (
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/output"
)

func TestASealDeadlineOutsideItsFrozenRangeIsRefusedBeforeItIsCommitted(t *testing.T) {
	for name, deadline := range map[string]time.Duration{
		"under the floor":  output.MinSealDeadline - time.Second,
		"over the ceiling": output.MaxSealDeadline + time.Second,
		"negative":         -time.Minute,
		"a whole day":      24 * time.Hour,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			c := h.admit(t).hold(t).finish(t, true)
			h.Coordinator.SealDeadline = deadline

			decision, err := advanceToTheSeal(t, c)
			if !errors.Is(err, output.ErrIncomplete) {
				t.Fatalf("a seal deadline of %s was accepted at %s: err=%v. Req 17 freezes the "+
					"range at %s..%s and this field is the only configuration site there is",
					deadline, decision.Transition, err, output.MinSealDeadline,
					output.MaxSealDeadline)
			}

			// And the refusal is before the write, not after it: the record
			// must not have begun sealing.
			if record := c.record(t); record.SealBegun {
				t.Errorf("the capture began sealing under a deadline the plane refuses. A " +
					"deadline committed and then rejected bounds the capture by a number " +
					"nobody approved")
			}
		})
	}
}

// The control. Without it every assertion above would also pass for a
// coordinator that refused every seal.
func TestTheFrozenDefaultAndTheEndsOfTheRangeAreAccepted(t *testing.T) {
	for name, deadline := range map[string]time.Duration{
		"unset, so the frozen default": 0,
		"exactly the floor":            output.MinSealDeadline,
		"exactly the ceiling":          output.MaxSealDeadline,
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			c := h.admit(t).hold(t).finish(t, true)
			h.Coordinator.SealDeadline = deadline

			if _, err := advanceToTheSeal(t, c); err != nil {
				t.Fatalf("a seal deadline of %s was refused: %v", deadline, err)
			}
		})
	}
}

// advanceToTheSeal runs the coordinator until it takes the seal transition and
// returns what that transition answered, error and all.
func advanceToTheSeal(t *testing.T, c *capture) (hangaroutput.Decision, error) {
	t.Helper()

	var taken []hangaroutput.Transition
	for i := 0; i < 12; i++ {
		decision, err := c.advanceOnce(t)
		taken = append(taken, decision.Transition)
		if decision.Transition == hangaroutput.TransitionBeginSeal {
			return decision, err
		}
		if err != nil {
			t.Fatalf("advancing (%s) after %v: %v", decision.Transition, taken, err)
		}
		if decision.Transition == hangaroutput.TransitionNone ||
			decision.Transition == hangaroutput.TransitionAwaitOutcome {
			break
		}
	}
	t.Fatalf("the coordinator never reached the seal: %v", taken)

	return hangaroutput.Decision{}, nil
}
