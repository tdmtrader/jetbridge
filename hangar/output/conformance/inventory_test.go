package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
)

// The sweep's page and cursor internals, against both substrates.
//
// These are Go and not scenarios because every one of them is either a page
// boundary, a continuation, or a fault -- the learning's own "stays Go" rows --
// and because the plan's Phase 7 framework rules that nothing in this phase
// moves to brine at all: adoption's precondition is elapsed publication grace,
// and Req 39 forbids configuring grace below maximum capture deadline plus an
// hour, so a chain that reached it would hang rather than fail.
//
// What is NOT a double here: the objects are published by the real publisher
// role into a real bucket (tier 2 is fake-gcs-server through the production
// hangar/gcs adapter), and the sweep is the real inventory role reading them
// back. Only the faults are injected, which is exactly what tier 1 exists for.

// stoppedClock is the pass-duration seam. It answers the same instant until it
// is moved, so a 30-second budget is reachable in a test without a test that
// takes 30 seconds.
type stoppedClock struct{ at time.Time }

func (clock *stoppedClock) Now() time.Time { return clock.at }

func startedCursor(t *testing.T) output.InventoryCursor {
	t.Helper()

	return output.InventoryCursor{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: testEpoch,
		CursorFence:     1,
		UpdatedAt:       output.NewTimestamp(fixedInstant),
	}
}

// publishTrees puts n distinct marked trees in the bucket and returns them in
// listing order.
func publishTrees(t *testing.T, tier substrate, namespace output.OutputNamespace, fills ...string) []output.PublishedObject {
	t.Helper()

	ctx := context.Background()
	role, _ := publisherFor(t, tier, namespace)

	published := make([]output.PublishedObject, 0, len(fills))
	for index, fill := range fills {
		body := canonicalBytes("tree " + fill)
		reservation := reservationFor(t, namespace, output.ReservationID(
			fmt.Sprintf("%08d-4444-4444-8444-444444444444", index+1)), digestOf(fill))
		object, err := role.EnsureObject(ctx, reservation, bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("publishing %s: %v", fill, err)
		}
		published = append(published, object)
	}

	return published
}

func sweepOver(t *testing.T, namespace output.OutputNamespace, store inventory.Store, clock output.Clock) *inventory.Inventory {
	t.Helper()

	sweep, err := inventory.New(namespace, store, clock)
	if err != nil {
		t.Fatalf("building the inventory: %v", err)
	}

	return sweep
}

// TestTheSweepContinuesLexicographicallyAndWrapsAtBucketEnd is Req 43's
// continuation and wrap, plus Req 44's page bound, in one walk.
//
// The assertion that matters is not "it saw everything" -- a sweep with no
// cursor at all would see everything on the first page if the page were large
// enough. It is that a page of two over five objects takes three passes, each
// page starts where the last one stopped, no object is returned twice, and the
// cursor's cycle counter advances exactly once, at the end.
func TestTheSweepContinuesLexicographicallyAndWrapsAtBucketEnd(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a", "2b", "3c", "4d", "5e")

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		budget := output.PageBudget{
			MaxObjects:       2,
			MaxMetadataBytes: output.MaxInventoryPageMetadataBytes,
			MaxDuration:      output.MaxInventoryPassDuration,
		}

		cursor := startedCursor(t)
		seen := []string{}
		passes := 0
		for cursor.Cycle == 0 {
			passes++
			if passes > 10 {
				t.Fatalf("the sweep did not wrap in %d passes; seen %v", passes, seen)
			}
			page, err := sweep.ListPage(ctx, cursor, budget)
			if err != nil {
				t.Fatalf("pass %d: %v", passes, err)
			}
			if len(page.Objects) > budget.MaxObjects {
				t.Fatalf("pass %d returned %d objects against a budget of %d",
					passes, len(page.Objects), budget.MaxObjects)
			}
			for _, object := range page.Objects {
				key := fmt.Sprintf("%s#%d", object.ObjectKey, object.Generation)
				for _, already := range seen {
					if already == key {
						t.Fatalf("pass %d returned %s a second time", passes, key)
					}
				}
				seen = append(seen, key)
			}
			cursor = page.Next
		}

		if passes != 3 {
			t.Errorf("five objects at two per page took %d passes, expected 3", passes)
		}
		if len(seen) != 5 {
			t.Errorf("the sweep saw %d objects, expected 5: %v", len(seen), seen)
		}
		if !sortedAscending(seen) {
			t.Errorf("the sweep did not walk lexicographically: %v", seen)
		}
		if cursor.Cycle != 1 {
			t.Errorf("the cursor is at cycle %d after one wrap, expected 1", cursor.Cycle)
		}
		if !cursor.AtCycleStart() {
			t.Errorf("a wrapped cursor kept the after-key %q; the next cycle must restart at the "+
				"validated output prefix so an object inserted behind it is revisited",
				cursor.AfterKey)
		}
	})
}

func sortedAscending(keys []string) bool {
	for index := 1; index < len(keys); index++ {
		if keys[index-1] >= keys[index] {
			return false
		}
	}

	return true
}

// TestAnObjectRecreatedAtTheSameKeyIsSweptAgain is why the cursor is a
// (key, generation) pair and not a key.
//
// A key alone cannot tell "already done" from "recreated while I was away", and
// the second is a new object at an old name. The store's own after-key
// semantics drop the resumed-from key, so without the generation half this
// object is invisible for a whole cycle.
func TestAnObjectRecreatedAtTheSameKeyIsSweptAgain(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		published := publishTrees(t, tier, namespace, "1a", "2b")

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		budget := output.PageBudget{
			MaxObjects:       1,
			MaxMetadataBytes: output.MaxInventoryPageMetadataBytes,
			MaxDuration:      output.MaxInventoryPassDuration,
		}

		first, err := sweep.ListPage(ctx, startedCursor(t), budget)
		if err != nil {
			t.Fatalf("first pass: %v", err)
		}
		if len(first.Objects) != 1 {
			t.Fatalf("the first page held %d objects, expected exactly 1", len(first.Objects))
		}
		stoppedAt := first.Objects[0]

		// Replace the object the cursor now names, out of band, the way a
		// retrying capture or an administrator would. The key is unchanged and
		// the generation is new.
		recreated := recreateAt(t, tier, stoppedAt.ObjectKey, published)
		if recreated <= stoppedAt.Generation {
			t.Fatalf("the recreated object is at generation %d, not past %d; the substrate does "+
				"not assign monotonic generations and this case cannot be expressed on it",
				recreated, stoppedAt.Generation)
		}

		second, err := sweep.ListPage(ctx, first.Next, budget)
		if err != nil {
			t.Fatalf("second pass: %v", err)
		}
		found := false
		for _, object := range second.Objects {
			if object.ObjectKey == stoppedAt.ObjectKey && object.Generation == recreated {
				found = true
			}
		}
		if !found {
			t.Errorf("the object recreated at %s reached generation %d and the next pass returned "+
				"%v. A cursor that is only a key cannot tell a finished object from a new one at "+
				"the same name, and Req 43 says the after-key is (key, generation) for exactly "+
				"this", stoppedAt.ObjectKey, recreated, describe(second.Objects))
		}
	})
}

// recreateAt overwrites one key with different marked content and returns the
// new generation.
func recreateAt(t *testing.T, tier substrate, key string, published []output.PublishedObject) int64 {
	t.Helper()

	ctx := context.Background()
	var marker output.ObjectMarker
	for _, object := range published {
		if strings.HasSuffix(key, string(object.Attributes.Ref.Digest)[len("sha256:"):]) {
			marker = object.Marker
		}
	}
	if marker.Version == "" {
		marker = published[0].Marker
	}

	writer := tier.client.Object(tier.bucket, key).NewWriter(ctx)
	writer.SetMetadata(marker.Metadata())
	if _, err := writer.Write(canonicalBytes("recreated at the same key")); err != nil {
		t.Fatalf("recreating %s: %v", key, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("recreating %s: %v", key, err)
	}

	return writer.Attrs().Generation
}

func describe(objects []output.InventoryObject) []string {
	described := make([]string, 0, len(objects))
	for _, object := range objects {
		described = append(described, fmt.Sprintf("%s#%d", object.ObjectKey, object.Generation))
	}

	return described
}

// TestAnObjectInsertedBehindTheCursorIsSweptNextCycle is Req 43's insertion
// rule, both directions in one case.
//
// An object inserted AHEAD of the cursor is reached in this cycle; one inserted
// BEHIND it is not, and is reached after the wrap. The second half is the one
// that matters: it is the reason the cursor wraps rather than declaring the
// bucket done.
func TestAnObjectInsertedBehindTheCursorIsSweptNextCycle(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "5e", "7f")

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)
		budget := output.PageBudget{
			MaxObjects:       1,
			MaxMetadataBytes: output.MaxInventoryPageMetadataBytes,
			MaxDuration:      output.MaxInventoryPassDuration,
		}

		first, err := sweep.ListPage(ctx, startedCursor(t), budget)
		if err != nil {
			t.Fatalf("first pass: %v", err)
		}
		if len(first.Objects) != 1 {
			t.Fatalf("the first page held %d objects, expected exactly 1", len(first.Objects))
		}

		// One key that sorts before everything already swept, and one that
		// sorts after. `0` and `9` are chosen against the hex digest alphabet.
		publishTrees(t, tier, namespace, "0a", "9d")

		behind, ahead := "", ""
		for _, object := range publishedKeys(t, tier, namespace) {
			if object < first.Next.AfterKey && behind == "" {
				behind = object
			}
			if object > first.Next.AfterKey && ahead == "" {
				ahead = object
			}
		}
		if behind == "" || ahead == "" {
			t.Fatalf("the fixture produced no key on one side of the cursor %q", first.Next.AfterKey)
		}

		cursor := first.Next
		sawAhead, sawBehindThisCycle := false, false
		for passes := 0; cursor.Cycle == 0 && passes < 12; passes++ {
			page, err := sweep.ListPage(ctx, cursor, budget)
			if err != nil {
				t.Fatalf("pass: %v", err)
			}
			for _, object := range page.Objects {
				if object.ObjectKey == ahead {
					sawAhead = true
				}
				if object.ObjectKey == behind {
					sawBehindThisCycle = true
				}
			}
			cursor = page.Next
		}

		if !sawAhead {
			t.Errorf("an object inserted ahead of the cursor at %q was never reached in the cycle "+
				"it was inserted into", first.Next.AfterKey)
		}
		if sawBehindThisCycle {
			t.Errorf("an object inserted behind the cursor at %q came back in the same cycle; the "+
				"after-key is not being honoured", first.Next.AfterKey)
		}
		if cursor.Cycle != 1 {
			t.Fatalf("the sweep did not wrap; cursor %+v", cursor)
		}

		page, err := sweep.ListPage(ctx, cursor, output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("the pass after the wrap: %v", err)
		}
		found := false
		for _, object := range page.Objects {
			if object.ObjectKey == behind {
				found = true
			}
		}
		if !found {
			t.Errorf("the object inserted behind the cursor was not swept after the wrap either; "+
				"it is stranded. Page: %v", describe(page.Objects))
		}
	})
}

func publishedKeys(t *testing.T, tier substrate, namespace output.OutputNamespace) []string {
	t.Helper()

	page, err := tier.client.List(context.Background(), tier.bucket, objectstore.ListRequest{
		Prefix: namespace.ListPrefix(), PageSize: output.MaxInventoryPageObjects,
	})
	if err != nil {
		t.Fatalf("listing the fixture: %v", err)
	}
	keys := make([]string, 0, len(page.Objects))
	for _, attrs := range page.Objects {
		keys = append(keys, attrs.Key)
	}

	return keys
}

// TestAnEmptyPrefixSweepsCleanlyAndDoesNotAdvanceIntoNothing is the empty-page
// case: a bucket with none of this plane's objects in it.
func TestAnEmptyPrefixSweepsCleanlyAndDoesNotAdvanceIntoNothing(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		page, err := sweep.ListPage(ctx, startedCursor(t), output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("sweeping an empty prefix: %v", err)
		}
		if len(page.Objects) != 0 || len(page.Debt) != 0 {
			t.Fatalf("an empty prefix produced %d objects and %d debt rows",
				len(page.Objects), len(page.Debt))
		}
		if !page.Complete {
			t.Error("an empty page is a complete pass; nothing stopped it")
		}
		if page.Next.Cycle != 1 {
			t.Errorf("an exhausted listing left the cursor at cycle %d, expected 1", page.Next.Cycle)
		}
		if page.Next.AfterKey != "" {
			t.Errorf("an empty page moved the after-key to %q", page.Next.AfterKey)
		}
	})
}

// TestPoisonedMetadataBecomesDebtAndTheSweepMovesPast is Req 44's whole point.
//
// One object whose marker cannot be decoded must not stop the keys after it.
// The control is asserted first -- the later object IS classified -- so a sweep
// that returned nothing at all could not pass the absence.
func TestPoisonedMetadataBecomesDebtAndTheSweepMovesPast(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a", "9f")

		keys := publishedKeys(t, tier, namespace)
		if len(keys) != 2 {
			t.Fatalf("the fixture published %d objects, expected 2", len(keys))
		}
		poisonAt(t, tier, keys[0], map[string]string{
			output.MarkerKeyVersion:       output.MarkerVersion,
			output.MarkerKeyScope:         string(namespace.Scope()),
			output.MarkerKeyDigest:        "not-a-digest",
			output.MarkerKeyReservationID: "not-a-uuid",
			output.MarkerKeyActivation:    "not-a-number",
			output.MarkerKeyCreatedAt:     "not-a-time",
		})

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		page, err := sweep.ListPage(ctx, startedCursor(t), output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("sweeping past poison: %v", err)
		}

		// The control: the object AFTER the poison was classified.
		classified := false
		for _, object := range page.Objects {
			if object.ObjectKey == keys[1] {
				classified = true
			}
		}
		if !classified {
			t.Fatalf("the object after the poisoned one was not classified; a poisoned object is "+
				"starving every key behind it. Page: %v", describe(page.Objects))
		}

		var recorded output.InventoryDebt
		for _, debt := range page.Debt {
			if debt.ObjectKey == keys[0] {
				recorded = debt
			}
		}
		if recorded.ObjectKey == "" {
			t.Fatalf("poisoned metadata produced no debt row; %d were recorded", len(page.Debt))
		}
		if recorded.Reason != output.DebtPoisonMetadata {
			t.Errorf("poisoned metadata was recorded as %q, expected %q",
				recorded.Reason, output.DebtPoisonMetadata)
		}
		if err := recorded.Validate(); err != nil {
			t.Errorf("the debt row does not validate: %v", err)
		}
		if len(recorded.Detail) > output.MaxDebtDetailBytes {
			t.Errorf("the debt detail is %d bytes, past the %d bound",
				len(recorded.Detail), output.MaxDebtDetailBytes)
		}
		for _, object := range page.Objects {
			if object.ObjectKey == keys[0] {
				t.Error("the poisoned object was also returned as a classified object; a poisoned " +
					"marker is debt, and a sweep that returned both would let a caller adopt it")
			}
		}
		// The cursor got PAST the poison: either it is now at or beyond that
		// key, or the pass ran the prefix out and wrapped, which is further
		// still. A cursor left behind it would replay it forever.
		if page.Next.Cycle == 0 && page.Next.AfterKey < keys[0] {
			t.Errorf("the cursor stopped at %q, before the poisoned object at %q, and did not "+
				"wrap; it would be replayed forever", page.Next.AfterKey, keys[0])
		}
	})
}

// TestAWrongVersionMarkerIsDebtAndNeverRelabelled is Req 45's other half: an
// object some other cohort marked is a deliberate statement and is left alone.
func TestAWrongVersionMarkerIsDebtAndNeverRelabelled(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a")
		keys := publishedKeys(t, tier, namespace)

		poisonAt(t, tier, keys[0], map[string]string{
			output.MarkerKeyVersion: "hangar-output-v99",
		})

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)
		page, err := sweep.ListPage(ctx, startedCursor(t), output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("sweeping: %v", err)
		}

		var recorded output.InventoryDebt
		for _, debt := range page.Debt {
			if debt.ObjectKey == keys[0] {
				recorded = debt
			}
		}
		if recorded.ObjectKey == "" {
			t.Fatalf("a wrong-version marker produced no debt; %d rows were recorded", len(page.Debt))
		}
		if recorded.Reason != output.DebtMarkerMismatch {
			t.Errorf("a wrong-version marker was recorded as %q, expected %q",
				recorded.Reason, output.DebtMarkerMismatch)
		}
		for _, object := range page.Objects {
			if object.ObjectKey == keys[0] {
				t.Errorf("an object marked by another cohort came back as a classified object "+
					"(managed=%v). It is debt: this plane never relabels, adopts or deletes one, "+
					"and returning it beside its debt row would let a caller adopt exactly what "+
					"was just recorded as another cohort's", object.Managed)
			}
		}
	})
}

// TestAnUnmarkedObjectIsUnmanagedAndNotDebt separates the two absences.
//
// Debt is work this deployment still owes. An unmarked object is not work: it
// is somebody else's object, recorded for diagnosis and never touched. A sweep
// that filed it as debt would grow an unbounded debt table out of a bucket
// somebody shared.
func TestAnUnmarkedObjectIsUnmanagedAndNotDebt(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a")
		keys := publishedKeys(t, tier, namespace)
		poisonAt(t, tier, keys[0], map[string]string{"unrelated": "metadata"})

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)
		page, err := sweep.ListPage(ctx, startedCursor(t), output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("sweeping: %v", err)
		}

		for _, debt := range page.Debt {
			if debt.ObjectKey == keys[0] {
				t.Errorf("an unmarked object was filed as %q debt. It is unmanaged: recorded for "+
					"diagnosis and never relabelled, adopted or deleted", debt.Reason)
			}
		}
		found := false
		for _, object := range page.Objects {
			if object.ObjectKey == keys[0] {
				found = true
				if object.Managed {
					t.Error("an unmarked object came back managed")
				}
			}
		}
		if !found {
			t.Error("an unmarked object was not reported at all; inventory records what it found")
		}
	})
}

func poisonAt(t *testing.T, tier substrate, key string, metadata map[string]string) {
	t.Helper()

	ctx := context.Background()
	writer := tier.client.Object(tier.bucket, key).NewWriter(ctx)
	writer.SetMetadata(metadata)
	if _, err := writer.Write(canonicalBytes("rewritten out of band")); err != nil {
		t.Fatalf("poisoning %s: %v", key, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("poisoning %s: %v", key, err)
	}
}

// TestMetadataLargerThanAWholePassBecomesDebtRatherThanReplayingForever is the
// bug Phase 5's reviewer named and left for this phase.
//
// A page that stops before an over-budget object and advances the cursor to the
// object before it replays that object on every pass, forever, and every key
// behind it with it. A budget is a stop condition; an object that cannot fit in
// ANY pass is a disposition.
func TestMetadataLargerThanAWholePassBecomesDebtRatherThanReplayingForever(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a", "9f")
		keys := publishedKeys(t, tier, namespace)

		budget := output.PageBudget{
			MaxObjects:       output.MaxInventoryPageObjects,
			MaxMetadataBytes: 1024,
			MaxDuration:      output.MaxInventoryPassDuration,
		}

		// A marker-shaped metadata map whose total size is larger than the
		// whole budget, and only just.
		//
		// The size is derived from the budget rather than picked, and that is a
		// CI finding rather than tidiness: an earlier 4 KiB value passed against
		// the in-process server on macOS and failed against the deployed one on
		// Linux with `xattr.Set … no space left on device`. fake-gcs-server
		// stores custom metadata as extended attributes, and ext4 keeps all of
		// an inode's xattrs in one block -- so "large" here has a ceiling that
		// has nothing to do with this plane's budget. What the case is about is
		// being over the BUDGET; a couple of hundred bytes over is the whole
		// assertion, and it is portable.
		bulky := map[string]string{
			output.MarkerKeyVersion: output.MarkerVersion,
			"hangar-bulk":           strings.Repeat("x", int(budget.MaxMetadataBytes)+200),
		}
		poisonAt(t, tier, keys[0], bulky)

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		page, err := sweep.ListPage(ctx, startedCursor(t), budget)
		if err != nil {
			t.Fatalf("sweeping: %v", err)
		}

		if page.Next.AfterKey < keys[0] {
			t.Fatalf("the cursor stopped at %q, before the over-budget object at %q. Every pass "+
				"from here re-reads it and never gets past it",
				page.Next.AfterKey, keys[0])
		}
		var recorded output.InventoryDebt
		for _, debt := range page.Debt {
			if debt.ObjectKey == keys[0] {
				recorded = debt
			}
		}
		if recorded.ObjectKey == "" {
			t.Fatalf("an object whose metadata exceeds a whole pass budget produced no debt row; "+
				"%d were recorded", len(page.Debt))
		}
		if recorded.Reason != output.DebtPoisonMetadata {
			t.Errorf("over-budget metadata was recorded as %q, expected %q",
				recorded.Reason, output.DebtPoisonMetadata)
		}

		// And the second object is reachable on the next pass rather than
		// stranded behind the first.
		next, err := sweep.ListPage(ctx, page.Next, budget)
		if err != nil {
			t.Fatalf("the pass after the over-budget object: %v", err)
		}
		reached := false
		for _, object := range next.Objects {
			if object.ObjectKey == keys[1] {
				reached = true
			}
		}
		if !reached && page.Next.Cycle == 0 {
			t.Errorf("the key behind the over-budget object was not reached on the next pass "+
				"either: %v", describe(next.Objects))
		}
	})
}

// TestAPassStopsAtItsDurationBudgetWithoutLosingWhatItClassified is the 30
// seconds of Req 44, reachable because the clock is a parameter.
func TestAPassStopsAtItsDurationBudgetWithoutLosingWhatItClassified(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a", "2b", "3c", "4d")

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		// The control: with the clock stopped, the whole prefix fits in one
		// pass. Without it, the case below proves nothing.
		whole, err := sweep.ListPage(ctx, startedCursor(t), output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("the control pass: %v", err)
		}
		if len(whole.Objects) != 4 || !whole.Complete {
			t.Fatalf("the control pass classified %d objects (complete=%v), expected 4 and true",
				len(whole.Objects), whole.Complete)
		}

		// Now a clock that runs 20 seconds per object against a 30-second
		// budget: the pass classifies some and stops, and the cursor names the
		// last one it actually classified.
		ticking := &tickingClock{at: fixedInstant, per: 20 * time.Second}
		limited := sweepOver(t, namespace, inventory.Restrict(tier.client), ticking)
		page, err := limited.ListPage(ctx, startedCursor(t), output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("the budgeted pass: %v", err)
		}
		if page.Complete {
			t.Error("a pass that stopped on its duration budget reported itself complete; the " +
				"cursor would wrap over objects it never read")
		}
		if len(page.Objects) == 0 {
			t.Fatal("the budgeted pass classified nothing at all; it cannot make progress")
		}
		if len(page.Objects) >= 4 {
			t.Errorf("the budgeted pass classified all %d objects; the 30-second bound did not "+
				"stop anything", len(page.Objects))
		}
		last := page.Objects[len(page.Objects)-1]
		if page.Next.AfterKey != last.ObjectKey || page.Next.AfterGeneration != last.Generation {
			t.Errorf("the cursor is at %q#%d and the last classified object was %q#%d; a cursor "+
				"past what was classified skips objects and one behind replays them",
				page.Next.AfterKey, page.Next.AfterGeneration, last.ObjectKey, last.Generation)
		}
		if page.Next.Cycle != 0 {
			t.Error("a pass cut short by a budget wrapped the cycle")
		}
	})
}

// tickingClock advances by a fixed interval on every reading, so a per-object
// cost is expressible without a sleep.
type tickingClock struct {
	at  time.Time
	per time.Duration
}

func (clock *tickingClock) Now() time.Time {
	clock.at = clock.at.Add(clock.per)

	return clock.at
}

// TestAListOutageStopsThePassWithoutAdvancingTheCursor is Req 44's "list
// failure stops without advancing": a short list is not the end of a bucket.
func TestAListOutageStopsThePassWithoutAdvancingTheCursor(t *testing.T) {
	memory := gcstest.NewMemory()
	namespace := namespaceFor(t, "outage-bucket")
	tier := substrate{name: "tier-1 (fault injection)", bucket: "outage-bucket", client: memory, memory: memory}
	publishTrees(t, tier, namespace, "1a", "2b")

	clock := &stoppedClock{at: fixedInstant}
	sweep := sweepOver(t, namespace, inventory.Restrict(memory), clock)

	// The control first: the sweep works before the fault is armed.
	cursor := startedCursor(t)
	healthy, err := sweep.ListPage(context.Background(), cursor, output.DefaultPageBudget())
	if err != nil {
		t.Fatalf("the control pass: %v", err)
	}
	if len(healthy.Objects) != 2 {
		t.Fatalf("the control pass classified %d objects, expected 2", len(healthy.Objects))
	}

	memory.Inject(gcstest.Faults{Unauthorized: true})
	page, err := sweep.ListPage(context.Background(), cursor, output.DefaultPageBudget())
	if err == nil {
		t.Fatalf("a listing outage produced no error; the pass returned %d objects",
			len(page.Objects))
	}
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Errorf("a 403 on the listing came back as %v, not %v", err, output.ErrUnauthorized)
	}
	if page.Next.AfterKey != "" || page.Next.Cycle != 0 {
		t.Errorf("a failed listing advanced the cursor to %+v. A short list is not the end of a "+
			"bucket, and a cursor moved by one would make absence authoritative", page.Next)
	}
	if page.Complete {
		t.Error("a failed listing reported a complete pass")
	}
}

// TestAStatOutageIsDebtAndNotAbsence keeps the other outage from becoming a
// fact. An exact-generation stat that does not answer says nothing about
// whether the object is there.
func TestAStatOutageIsDebtAndNotAbsence(t *testing.T) {
	memory := gcstest.NewMemory()
	namespace := namespaceFor(t, "stat-outage-bucket")
	tier := substrate{name: "tier-1 (fault injection)", bucket: "stat-outage-bucket", client: memory, memory: memory}
	published := publishTrees(t, tier, namespace, "1a")

	clock := &stoppedClock{at: fixedInstant}
	sweep := sweepOver(t, namespace, inventory.Restrict(memory), clock)
	ref := published[0].Attributes.Ref

	// Control: the stat answers.
	if _, err := sweep.StatExactObject(context.Background(), ref); err != nil {
		t.Fatalf("the control stat: %v", err)
	}

	memory.Inject(gcstest.Faults{StatTimeout: true})
	_, err := sweep.StatExactObject(context.Background(), ref)
	if err == nil {
		t.Fatal("a timed-out stat answered")
	}
	if errors.Is(err, output.ErrNotFound) {
		t.Errorf("a timed-out stat came back as %v. An outage is not absence, and an inventory "+
			"that recorded it as one would authorize a reclaim of an object that is still there",
			output.ErrNotFound)
	}
	if !errors.Is(err, output.ErrTimeout) && !errors.Is(err, output.ErrInfrastructure) {
		t.Errorf("a timed-out stat came back as %v, which is neither a timeout nor an "+
			"infrastructure failure", err)
	}
}

// TestAStaleEpochCursorOwnerCannotReserveAPage is Req 42: only the current
// fencing epoch may reserve a page or advance the cursor.
func TestAStaleEpochCursorOwnerCannotReserveAPage(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		// The control: this epoch's cursor is accepted.
		if _, err := sweep.ListPage(ctx, startedCursor(t), output.DefaultPageBudget()); err != nil {
			t.Fatalf("the current epoch's cursor was refused: %v", err)
		}

		stale := startedCursor(t)
		stale.ActivationEpoch = testEpoch - 1
		page, err := sweep.ListPage(ctx, stale, output.DefaultPageBudget())
		if err == nil {
			t.Fatalf("a cursor from epoch %d reserved a page in epoch %d and returned %d objects",
				stale.ActivationEpoch, testEpoch, len(page.Objects))
		}
		if !errors.Is(err, output.ErrConflict) {
			t.Errorf("a stale-epoch cursor was refused with %v, expected %v", err, output.ErrConflict)
		}
		if page.Next.Cycle != 0 || page.Next.AfterKey != "" {
			t.Errorf("a refused pass returned a moved cursor: %+v", page.Next)
		}
	})
}

// TestACorruptAfterKeyIsDebtAndRestartsAtTheOutputPrefix is Req 44's third
// named case. A cursor that does not validate is not authoritative absence: it
// is a fact about the cursor, recorded, and the sweep starts again from the
// validated prefix rather than from nothing.
func TestACorruptAfterKeyIsDebtAndRestartsAtTheOutputPrefix(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a", "2b")

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)

		corrupt := startedCursor(t)
		corrupt.AfterKey = ""
		corrupt.AfterGeneration = 91

		restarted, debt, err := sweep.RecoverCursor(corrupt)
		if err != nil {
			t.Fatalf("recovering a corrupt cursor: %v", err)
		}
		if debt.Reason != output.DebtCorruptCursor {
			t.Errorf("a corrupt after-key was recorded as %q, expected %q",
				debt.Reason, output.DebtCorruptCursor)
		}
		if err := debt.Validate(); err != nil {
			t.Errorf("the corruption debt row does not validate: %v", err)
		}
		if !restarted.AtCycleStart() {
			t.Errorf("recovery left the cursor at %q; Req 44 restarts at the validated output "+
				"prefix", restarted.AfterKey)
		}
		if restarted.ActivationEpoch != corrupt.ActivationEpoch ||
			restarted.CursorFence != corrupt.CursorFence {
			t.Errorf("recovery changed the cursor's epoch or fence: %+v", restarted)
		}
		if err := restarted.Validate(); err != nil {
			t.Errorf("the restarted cursor does not validate: %v", err)
		}

		page, err := sweep.ListPage(ctx, restarted, output.DefaultPageBudget())
		if err != nil {
			t.Fatalf("sweeping from the restarted cursor: %v", err)
		}
		if len(page.Objects) != 2 {
			t.Errorf("the restart classified %d objects, expected the whole prefix (2): %v",
				len(page.Objects), describe(page.Objects))
		}

		// A valid cursor is not "recovered" into a restart: that would make
		// every pass start over.
		if _, _, err := sweep.RecoverCursor(startedCursor(t)); err == nil {
			t.Error("a valid cursor was accepted for corruption recovery; a recovery that " +
				"accepted a healthy cursor would restart every sweep from the prefix")
		}
	})
}

// TestContinuousArrivalsCannotStarveTheSweep is the liveness half at the page
// level: objects keep arriving while the sweep runs, and it still finishes a
// cycle rather than chasing the tail forever.
func TestContinuousArrivalsCannotStarveTheSweep(t *testing.T) {
	eachSubstrate(t, func(t *testing.T, tier substrate) {
		ctx := context.Background()
		namespace := namespaceFor(t, tier.bucket)
		publishTrees(t, tier, namespace, "1a", "2b")

		clock := &stoppedClock{at: fixedInstant}
		sweep := sweepOver(t, namespace, inventory.Restrict(tier.client), clock)
		budget := output.PageBudget{
			MaxObjects:       1,
			MaxMetadataBytes: output.MaxInventoryPageMetadataBytes,
			MaxDuration:      output.MaxInventoryPassDuration,
		}

		cursor := startedCursor(t)
		arrivals := []string{"3c", "4d", "5e", "6a", "7b"}
		for pass := 0; cursor.Cycle == 0; pass++ {
			if pass > 20 {
				t.Fatalf("the sweep never wrapped while objects kept arriving; cursor %+v", cursor)
			}
			if pass < len(arrivals) {
				// One new object per pass, at a key AHEAD of the cursor, which
				// is the harder direction: a sweep that never wrapped would
				// chase these forever.
				publishTrees(t, tier, namespace, arrivals[pass])
			}
			page, err := sweep.ListPage(ctx, cursor, budget)
			if err != nil {
				t.Fatalf("pass %d: %v", pass, err)
			}
			cursor = page.Next
		}
	})
}
