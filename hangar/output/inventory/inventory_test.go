package inventory_test

// The inventory's own refusals and its cursor recovery, decided from the
// cursor and the budget before any listing is issued. The sweep itself --
// pagination, debt, the cursor's advance -- is proved over both substrate
// tiers in the conformance suite; this file is about the seam the pass
// dials, and the recorder is what makes "before any listing" a fact.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
	testsupport "github.com/concourse/concourse/hangar/output/testsupport"
)

const (
	bucket = "inventory-spec"
	epoch  = executioncontrol.ActivationEpoch(7)
)

var clock = output.ClockFunc(func() time.Time { return testsupport.FixedInstant })

func namespaceFor(t *testing.T) output.OutputNamespace {
	return testsupport.Namespace(t, bucket, "tenant-a", epoch)
}

// role builds an inventory over a recorded tier-1 store.
func role(t *testing.T, namespace output.OutputNamespace) (*inventory.Inventory, *gcstest.Recorder) {
	t.Helper()

	_, recorder := testsupport.RecordedMemory(bucket)
	built, err := inventory.New(namespace, inventory.Restrict(recorder), clock)
	if err != nil {
		t.Fatalf("building the inventory: %v", err)
	}

	return built, recorder
}

func TestNewRefusesAnIncompleteRole(t *testing.T) {
	namespace := namespaceFor(t)
	store := inventory.Restrict(gcstest.NewMemory())

	for name, build := range map[string]func() (*inventory.Inventory, error){
		"a zero namespace": func() (*inventory.Inventory, error) {
			return inventory.New(output.OutputNamespace{}, store, clock)
		},
		"no store": func() (*inventory.Inventory, error) {
			return inventory.New(namespace, nil, clock)
		},
		"no clock": func() (*inventory.Inventory, error) {
			return inventory.New(namespace, store, nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			built, err := build()
			if !errors.Is(err, output.ErrIncomplete) {
				t.Fatalf("expected ErrIncomplete, got %v", err)
			}
			if built != nil {
				t.Error("a refused constructor still returned an inventory")
			}
		})
	}

	if _, err := inventory.New(namespace, store, clock); err != nil {
		t.Fatalf("the complete shape was refused: %v", err)
	}
}

func TestListPageRefusesBeforeTheStoreIsReached(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t)

	// The cursor carries the epoch it was reserved under. A cursor from
	// another epoch is another namespace's position, and listing from it
	// would sweep this prefix from a point that means nothing here.
	t.Run("a cursor from another epoch", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.ListPage(ctx, testsupport.Cursor(epoch+1), output.DefaultPageBudget())
		if !errors.Is(err, output.ErrConflict) {
			t.Errorf("expected ErrConflict, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a cursor with no fence", func(t *testing.T) {
		built, recorder := role(t, namespace)
		unfenced := testsupport.Cursor(epoch)
		unfenced.CursorFence = 0
		_, err := built.ListPage(ctx, unfenced, output.DefaultPageBudget())
		if !errors.Is(err, output.ErrIncomplete) {
			t.Errorf("expected ErrIncomplete, got %v", err)
		}
		testsupport.ExpectNoRPC(t, recorder)
	})

	t.Run("a budget with no stop condition", func(t *testing.T) {
		built, recorder := role(t, namespace)
		_, err := built.ListPage(ctx, testsupport.Cursor(epoch), output.PageBudget{})
		if err == nil {
			t.Error("an empty budget was accepted; a pass with no bound is a pass nothing checks")
		}
		testsupport.ExpectNoRPC(t, recorder)
	})
}

func TestRecoverCursorRefusesAHealthyCursor(t *testing.T) {
	namespace := namespaceFor(t)
	built, recorder := role(t, namespace)

	healthy := testsupport.Cursor(epoch)
	healthy.AfterKey = namespace.ListPrefix() + "somewhere"
	healthy.AfterGeneration = 4

	_, _, err := built.RecoverCursor(healthy)
	if !errors.Is(err, output.ErrConflict) {
		t.Errorf("expected ErrConflict, got %v. Recovering a cursor that validates would restart "+
			"every sweep at the prefix, and no object past the first page would ever be reached", err)
	}
	testsupport.ExpectNoRPC(t, recorder)
}

func TestRecoverCursorRestartsAtThePrefixAndRecordsTheClaimedPosition(t *testing.T) {
	namespace := namespaceFor(t)
	built, recorder := role(t, namespace)

	// A generation with no after-key orders against nothing: the one corrupt
	// shape a restart at the prefix repairs, with no key to name.
	corrupt := testsupport.Cursor(epoch)
	corrupt.AfterGeneration = 9
	corrupt.Cycle = 3
	if corrupt.Validate() == nil {
		t.Fatal("the fixture cursor validates; the case is about a cursor that does not")
	}

	restarted, debt, err := built.RecoverCursor(corrupt)
	if err != nil {
		t.Fatalf("recovering: %v", err)
	}
	testsupport.ExpectNoRPC(t, recorder)

	if !restarted.AtCycleStart() {
		t.Errorf("the restarted cursor is at %q#%d, not at the cycle start",
			restarted.AfterKey, restarted.AfterGeneration)
	}
	if restarted.Cycle != corrupt.Cycle {
		t.Errorf("the restart changed the cycle from %d to %d; a restart is a position, not a "+
			"completed sweep", corrupt.Cycle, restarted.Cycle)
	}
	if restarted.ActivationEpoch != epoch || restarted.CursorFence != corrupt.CursorFence {
		t.Errorf("the restart changed the owner: epoch %d fence %d",
			restarted.ActivationEpoch, restarted.CursorFence)
	}
	if err := restarted.Validate(); err != nil {
		t.Errorf("the restarted cursor does not validate: %v", err)
	}

	if debt.Reason != output.DebtCorruptCursor {
		t.Errorf("debt reason %q, expected %q", debt.Reason, output.DebtCorruptCursor)
	}
	// The debt names the position the cursor CLAIMED. It claimed no key, so
	// the only honest object identity is the output prefix itself; inventing
	// a key would be recording a fact about an object nothing observed.
	if debt.ObjectKey != namespace.ListPrefix() {
		t.Errorf("debt names %q; with no after-key the position is the output prefix %q",
			debt.ObjectKey, namespace.ListPrefix())
	}
	if debt.Generation != corrupt.AfterGeneration {
		t.Errorf("debt names generation %d, the cursor claimed %d", debt.Generation, corrupt.AfterGeneration)
	}
	if debt.ActivationEpoch != epoch || debt.Attempts != 1 || debt.Detail == "" {
		t.Errorf("debt is incomplete: %+v", debt)
	}
	if err := debt.Validate(); err != nil {
		t.Errorf("the debt row does not validate: %v", err)
	}
}

func TestRecoverCursorRefusesCorruptionARestartDoesNotRepair(t *testing.T) {
	built, recorder := role(t, namespaceFor(t))

	// No epoch at all is not a bad position; it is a cursor with no owner,
	// and restarting it at the prefix would sweep under nobody's authority.
	_, _, err := built.RecoverCursor(testsupport.Cursor(0))
	if !errors.Is(err, output.ErrCorrupt) {
		t.Errorf("expected ErrCorrupt, got %v", err)
	}
	testsupport.ExpectNoRPC(t, recorder)
}

func TestStatExactObjectAnswersAbsenceAsNotFound(t *testing.T) {
	ctx := context.Background()
	namespace := namespaceFor(t)
	built, recorder := role(t, namespace)

	_, err := built.StatExactObject(ctx, namespace.Ref(testsupport.Digest("ab"), 1))
	if !errors.Is(err, output.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if kinds := recorder.Kinds(); len(kinds) != 1 || kinds[0] != "objects.get(metadata)" {
		t.Errorf("a stat issued %v; it is a metadata get and nothing else", kinds)
	}
}
