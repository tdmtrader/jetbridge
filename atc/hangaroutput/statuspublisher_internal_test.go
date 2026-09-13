package hangaroutput

import (
	"sort"
	"testing"
	"time"

	"github.com/concourse/concourse/hangar/output"
)

// What the publisher emits a lease series FOR.
//
// statusEvent is the whole of that decision, so it is asserted here rather than
// through the metric emitter, which only ranges over the map this builds.
//
// The case that matters is the first one: a plane installed five minutes ago,
// healthy, every controller starting up and none of them holding a lease yet.
// Emitting -1 for all nine schema kinds put five permanent warnings on that
// plane -- HangarOutputOperationLeaseUnheld, ten minutes after install, "its
// controller is not running", about five kinds that have no controller to run.
// The four that do are the four that are asserted.
func TestTheLeaseSeriesCoverExactlyTheKindsAWorkloadOwns(t *testing.T) {
	want := map[string]bool{}
	for _, kind := range OwnedOperationKinds() {
		want[string(kind)] = true
	}
	if len(want) < 4 {
		t.Fatalf("OwnedOperationKinds is %d kinds; it collapsed and this rule would pass over "+
			"almost nothing", len(want))
	}

	t.Run("a freshly installed plane, no lease held by anybody", func(t *testing.T) {
		event := statusEvent(Status{Leases: map[output.OperationKind]time.Duration{}})

		assertLeaseKinds(t, event.LeaseRemainingSeconds, want)
		for kind, value := range event.LeaseRemainingSeconds {
			if value != -1 {
				t.Errorf("%s reports %v with no lease held; -1 is what says \"no owner at all\", "+
					"and 0 reads as \"expired right now\"", kind, value)
			}
		}
	})

	t.Run("every owned controller holding its lease", func(t *testing.T) {
		leases := map[output.OperationKind]time.Duration{}
		for _, kind := range OwnedOperationKinds() {
			leases[kind] = 90 * time.Second
		}
		event := statusEvent(Status{Leases: leases})

		assertLeaseKinds(t, event.LeaseRemainingSeconds, want)
		for kind, value := range event.LeaseRemainingSeconds {
			if value != 90 {
				t.Errorf("%s reports %v seconds remaining on a 90s lease", kind, value)
			}
		}
	})

	// A lease row for a kind nothing runs -- left behind by a one-off, or by an
	// operator experimenting -- is not a reason to start asserting liveness
	// about it. The series set is decided by the wiring, not by what happens to
	// be in the table.
	t.Run("a stray lease row for an unowned kind adds no series", func(t *testing.T) {
		leases := map[output.OperationKind]time.Duration{}
		for _, kind := range OwnedOperationKinds() {
			leases[kind] = time.Minute
		}
		for kind := range UnownedOperationKinds() {
			leases[kind] = time.Minute
		}
		event := statusEvent(Status{Leases: leases})

		assertLeaseKinds(t, event.LeaseRemainingSeconds, want)
	})
}

func assertLeaseKinds(t *testing.T, got map[string]float64, want map[string]bool) {
	t.Helper()

	var extra, missing []string
	for kind := range got {
		if !want[kind] {
			extra = append(extra, kind)
		}
	}
	for kind := range want {
		if _, ok := got[kind]; !ok {
			missing = append(missing, kind)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)

	if len(extra) > 0 {
		t.Errorf("the publisher emits a lease series for %v, which no workload takes a lease "+
			"for.\n\nHangarOutputOperationLeaseUnheld alerts on -1 after ten minutes and says "+
			"the kind's controller is not running. For a kind with no controller that is a "+
			"permanent warning on a healthy plane, and an alert that is on at install is the "+
			"one an operator silences -- along with the four that mean something.", extra)
	}
	if len(missing) > 0 {
		t.Errorf("the publisher emits no lease series for %v, which a workload does take a "+
			"lease for.\n\nThat worker's liveness is now unwatched: it can stop holding its "+
			"lease and nothing says so.", missing)
	}
}
