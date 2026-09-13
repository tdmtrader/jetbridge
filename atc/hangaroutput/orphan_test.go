package hangaroutput_test

import (
	"context"
	"errors"
	"testing"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
)

// The terminal orphan, end to end, against the real daemon and real PostgreSQL.
//
// Phase 5 left this transition as a loud refusal, and said why: there was no
// durable state to settle into. `settlement_is_earned` wants a release
// acknowledgement, and the release guard refused one past the irreversible
// publish point, so a capture that failed there could never settle -- keeping
// its incarnation pinned on a node and its correlation shielded from orphan
// adoption forever.
//
// Phase 7 removes the special case rather than adding a state. A terminal
// capture releases the HOLD on either side of the point (the bytes stay; the
// Phase 5 ruling), the release earns settlement, and the object it created is
// left exactly where it is -- marked, uncorrelated, and adoptable by the sweep
// once this reservation is terminal and publication grace has elapsed (Req 40).
//
// The state is reached here the way production reaches it: the object is
// created, the publish answer is lost so the point is durably recorded, and the
// owner then records a terminal failure instead of retrying.
func TestATerminalOrphanSettlesByReleasingTheSourceItSealed(t *testing.T) {
	h := newHarness(t)
	c := h.admit(t).hold(t).finish(t, true)

	// Stage 2, seal, confirm, resolve.
	for i := 0; i < 4; i++ {
		if _, err := c.advanceOnce(t); err != nil {
			t.Fatalf("advancing: %v", err)
		}
	}

	// The object lands and the answer is lost, which is what records the
	// irreversible publish point.
	h.Dialer.LoseAfter = "publish"
	if _, err := c.advanceOnce(t); !errors.Is(err, lostAnswer) {
		t.Fatalf("the lost upload response was not reported: %v", err)
	}

	past := c.record(t)
	if !past.PastIrreversiblePublishPoint {
		t.Fatal("the create attempt was not recorded, so this spec is not about a terminal orphan")
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Fatalf("the bucket holds %d objects, expected the one this capture created: %v",
			len(keys), keys)
	}

	// The owner gives up instead of retrying: Req 40's "or records terminal
	// failure" arm.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.RecordTerminalCaptureFailure(context.Background(),
		db.HangarOutputTx{Tx: tx}, past.ReservationID, 1, "seal_unconfirmed"); err != nil {
		_ = tx.Rollback()
		t.Fatalf("recording the terminal failure: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("committing the terminal failure: %v", err)
	}

	// The control: this really is the settle_orphan branch, so what follows is
	// that transition's behaviour and not a mis-selection.
	decision, err := hangaroutput.Decide(c.record(t))
	if err != nil {
		t.Fatalf("deciding: %v", err)
	}
	if decision.Transition != hangaroutput.TransitionSettleOrphan {
		t.Fatalf("a terminal capture past the publish point selects %q", decision.Transition)
	}

	c.advance(t)

	final := c.record(t)
	if !final.ReleaseAcknowledged {
		t.Fatal("the terminal orphan never released the source it sealed; a held source is " +
			"exempt from payload cleanup, sweep and reuse, so the incarnation is pinned forever")
	}
	if !final.Settled {
		t.Fatal("the terminal orphan is still unsettled, so its correlation is shielded from " +
			"orphan adoption for the life of the deployment")
	}

	// And it settled without inventing anything. No receipt, no lifecycle row:
	// what is in the bucket is a marked orphan for the sweep, not a result.
	if final.Receipt != nil {
		t.Error("a terminal orphan registered a receipt")
	}
	var lifecycles int
	if err := h.Conn.QueryRow(`
		SELECT count(*) FROM hangar_exact_lifecycles WHERE digest = $1`,
		string(past.Digest)).Scan(&lifecycles); err != nil {
		t.Fatalf("counting lifecycles: %v", err)
	}
	if lifecycles != 0 {
		t.Errorf("a terminal orphan recorded %d lifecycle row(s) for an object no receipt "+
			"correlates", lifecycles)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("settling the orphan changed the bucket: %v", keys)
	}

	// The logical reservation is terminal, which is the half that lets the
	// sweep adopt that object rather than shielding it forever.
	var logical string
	if err := h.Conn.QueryRow(`
		SELECT state FROM hangar_logical_reservations WHERE reservation_id = $1`,
		string(past.ReservationID)).Scan(&logical); err != nil {
		t.Fatalf("reading the logical reservation: %v", err)
	}
	if logical != "terminal" {
		t.Errorf("the logical reservation of a terminally failed capture is %q; an unresolved "+
			"one shields its correlation from adoption forever", logical)
	}

	// The bytes stay on the node. A release releases the hold.
	if !c.sourceStillThere() {
		t.Error("settling the orphan deleted the producer's output from the node")
	}
}
