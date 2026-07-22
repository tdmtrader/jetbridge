package atc_test

import (
	"testing"

	"github.com/concourse/concourse/atc"
)

func TestAgentSnapshotLifecycleComponentNamesAreStable(t *testing.T) {
	if atc.ComponentAgentSnapshotGC != "agent_snapshot_gc" {
		t.Fatalf("GC component name = %q", atc.ComponentAgentSnapshotGC)
	}
	if atc.ComponentAgentSnapshotRepair != "agent_snapshot_repair" {
		t.Fatalf("repair component name = %q", atc.ComponentAgentSnapshotRepair)
	}
}
