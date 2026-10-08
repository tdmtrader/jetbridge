package hangaroutput

import (
	"testing"

	"github.com/concourse/concourse/hangar/output"
)

// What the publisher emits: every member of the closed violation vocabulary,
// zeroes included, and the residue a drain waits on.
func TestTheStatusEventCarriesEveryViolationClassAndTheResidue(t *testing.T) {
	event := statusEvent(Status{
		Enabled: true,
		Counts:  output.PlaneCounts{PendingCaptures: 2, OpenClaims: 1},
	})

	for _, violation := range output.IntegrityViolations() {
		if _, ok := event.Violations[string(violation)]; !ok {
			t.Errorf("no %s series: a gauge that appears only when nonzero is one an alert "+
				"cannot tell from a scrape that did not happen", violation)
		}
	}
	if !event.Enabled {
		t.Error("the in-service flag was not carried")
	}
	if event.PendingCaptures != 2 || event.Residue != 3 {
		t.Errorf("the residue was not carried: pending %d, residue %d", event.PendingCaptures, event.Residue)
	}
}
