package hangaroutput_test

import (
	"errors"
	"testing"

	"github.com/concourse/concourse/hangar/output"
)

// A collision at a server-derived key is bounded by the CAPTURE DEADLINE.
//
// The publisher's typed conflict is the honest answer for an out-of-band writer:
// the object at the key is not this capture's bytes, and the bytes may be put
// back, so the retry is right and every recovery pass repeats it. What was
// missing until this phase is an END -- nothing read `capture_deadline_at`, so
// "retried until the capture deadline" was in fact "retried until somebody gives
// up", and the source stayed held on one node for as long as nobody looked.
//
// Both halves are asserted here, and the control is first: inside the deadline
// the coordinator RETRIES, and only past it does the capture become terminal.
// A test with only the terminal half would pass on a coordinator that
// terminalized the first collision it ever saw, which is the opposite mistake
// and a worse one -- the bytes really may be put back.

func TestACollisionInsideTheDeadlineIsRetriedAndPastItIsTerminal(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2, seal, confirm, resolve: everything up to the publish.
	for i := 0; i < 4; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}

	h.Dialer.CollideOnPublish = true

	// THE CONTROL. The deadline is an hour away, so the collision is retried:
	// the coordinator reports the conflict and the capture is still live.
	if _, err := c.advanceOnce(t); !errors.Is(err, output.ErrConflict) {
		t.Fatalf("a collision inside the deadline was not reported as a conflict: %v", err)
	}
	record := c.record(t)
	if record.State == output.CaptureStateFailed {
		t.Fatal("a collision INSIDE the capture deadline was made terminal. The bytes at a " +
			"server-derived key may still be put back by whoever put the wrong ones there, " +
			"and giving up on the first conflict is how a recoverable capture is thrown away")
	}
	if record.TerminalFailure != "" {
		t.Fatalf("a terminal failure was recorded inside the deadline: %q",
			record.TerminalFailure)
	}

	// Now move the deadline into the past, on the DATABASE clock -- which is
	// where every deadline in this plane is evaluated, because a coordinator
	// whose own clock had drifted forward would terminalize a capture that is
	// still entitled to register.
	ageTheCaptureDeadline(t, h, c)

	if _, err := c.advanceOnce(t); err != nil {
		t.Fatalf("advancing past the deadline: %v", err)
	}

	record = c.record(t)
	if record.State != output.CaptureStateFailed {
		t.Fatalf("the capture is %s past its deadline with an unresolvable collision; it is "+
			"bounded by the deadline rather than by attention", record.State)
	}
	if record.TerminalFailure != "collision" {
		t.Errorf("the terminal failure is %q and not \"collision\"", record.TerminalFailure)
	}
}

// ageTheCaptureDeadline moves one capture's deadline into the past, and says
// plainly that it is moving a clock.
//
// Two schema guards have to be suspended to do it, and both are load-bearing:
// the predeclaration is IMMUTABLE, and the Stage 2 trigger refuses a reservation
// whose deadline does not match the predeclaration's. Between them they are
// requirement 40's last sentence -- no reservation is extended past its original
// deadline merely to avoid collection -- and nothing in production writes this
// column after the predeclaration at all.
//
// The alternative is to wait the schema's own minimum of one hour, which
// convention 9 forbids and which would make this spec a hang rather than a
// failure. Suspending the guards and saying so is the honest form; suspending
// them quietly would make this look like a spec that reached the state by
// legitimate means.
func ageTheCaptureDeadline(t *testing.T, h *harness, c *capture) {
	t.Helper()

	for _, statement := range []string{
		`ALTER TABLE hangar_handoff_predeclarations DISABLE TRIGGER hangar_predeclaration_immutability_guard`,
		`ALTER TABLE hangar_capture_reservations DISABLE TRIGGER hangar_stage_two_matches_predeclaration`,
	} {
		if _, err := h.Conn.Exec(statement); err != nil {
			t.Fatalf("suspending a schema guard: %v", err)
		}
	}
	defer func() {
		for _, statement := range []string{
			`ALTER TABLE hangar_handoff_predeclarations ENABLE TRIGGER hangar_predeclaration_immutability_guard`,
			`ALTER TABLE hangar_capture_reservations ENABLE TRIGGER hangar_stage_two_matches_predeclaration`,
		} {
			if _, err := h.Conn.Exec(statement); err != nil {
				t.Fatalf("restoring a schema guard: %v", err)
			}
		}
	}()

	// Both timestamps move TOGETHER, by whatever interval puts the deadline one
	// minute in the past. The deadline is bounded RELATIVE to `created_at` --
	// between one hour and seven days -- so shifting one of them is a
	// configuration the schema refuses, and shifting both is the same capture,
	// older. Nothing is extended, which is the half requirement 40 cares about.
	//
	// The interval is computed from the row rather than written as a literal:
	// a literal would encode this harness's admission offset, and the spec
	// would go quietly green the day that offset changed.
	var shifted int64
	if err := h.Conn.QueryRow(`
		WITH aged AS (
			UPDATE hangar_handoff_predeclarations
			   SET created_at = created_at - (capture_deadline_at - now() + interval '1 minute'),
			       capture_deadline_at = now() - interval '1 minute'
			 WHERE handoff_id = $1
			 RETURNING 1
		)
		SELECT count(*) FROM aged`, string(c.Handoff)).Scan(&shifted); err != nil {
		t.Fatalf("ageing the predeclaration: %v", err)
	}
	if shifted != 1 {
		t.Fatalf("%d predeclarations were aged; the handoff is not the one this spec admitted",
			shifted)
	}
	if _, err := h.Conn.Exec(`
		UPDATE hangar_capture_reservations r
		   SET capture_deadline_at = p.capture_deadline_at
		  FROM hangar_handoff_predeclarations p
		 WHERE p.handoff_id = r.handoff_id AND r.handoff_id = $1`,
		string(c.Handoff)); err != nil {
		t.Fatalf("ageing the reservation's deadline: %v", err)
	}
}
