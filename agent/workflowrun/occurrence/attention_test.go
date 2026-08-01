package occurrence

import (
	"testing"
	"time"
)

func at(minute int) time.Time {
	return time.Date(2026, 7, 31, 10, minute, 0, 0, time.UTC)
}

func entry(runID int64, minute int, nodeID string, status Status) ChainEntry {
	return ChainEntry{
		RunID:        runID,
		RunCreatedAt: at(minute),
		Occurrence:   NodeOccurrence{NodeID: nodeID, Status: status},
	}
}

func TestResolveEffectiveLaterSuccessClearsEarlierFailure(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{
		entry(1, 0, "implement", StatusFailed),
		entry(2, 5, "implement", StatusSucceeded),
	})

	if len(effective) != 1 {
		t.Fatalf("expected one effective occurrence, got %+v", effective)
	}
	if effective[0].Status != StatusSucceeded {
		t.Fatalf("expected the later success to win, got %q", effective[0].Status)
	}
	if effective[0].NeedsAttention {
		t.Fatal("a resolved node must not need attention")
	}
}

func TestResolveEffectiveKeepsActiveAlongsideTerminal(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{
		entry(1, 0, "implement", StatusFailed),
		entry(2, 5, "implement", StatusRunning),
	})

	if len(effective) != 2 {
		t.Fatalf("expected the active occurrence to be retained beside the terminal one, got %+v", effective)
	}

	var sawRunning bool
	for _, resolved := range effective {
		if resolved.Status == StatusRunning {
			sawRunning = true
			if !resolved.NeedsAttention {
				t.Fatal("a running node is attention-worthy")
			}
		}
	}
	if !sawRunning {
		t.Fatal("expected a running effective occurrence")
	}
}

// An in-flight retry is already addressing the earlier failure, so the failure
// is not independently actionable. The design enumerates "failed then retry
// running" as the single outcome "running, and attention-worthy", distinct
// from "failed with no successful continuation". Both occurrences stay in the
// effective set — nothing is erased from history — but only the live one asks
// for action.
func TestResolveEffectiveActiveRetrySupersedesEarlierFailureForAttention(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{
		entry(1, 0, "implement", StatusFailed),
		entry(2, 5, "implement", StatusRunning),
	})

	for _, resolved := range effective {
		if resolved.Status == StatusFailed && resolved.NeedsAttention {
			t.Fatalf("a failure with a retry in flight is not independently actionable: %+v", resolved)
		}
	}
}

func TestResolveEffectiveBranchingRetriesTakeTheLatest(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{
		entry(1, 0, "implement", StatusFailed),
		entry(2, 5, "implement", StatusSucceeded),
		entry(3, 3, "implement", StatusFailed),
	})

	if len(effective) != 1 || effective[0].Status != StatusSucceeded || effective[0].RunID != 2 {
		t.Fatalf("expected the latest-created terminal occurrence to win, got %+v", effective)
	}
}

// Runs created at the same instant must still resolve to the same answer every
// time, so run ID breaks the tie rather than map or slice order.
func TestResolveEffectiveBreaksTimestampTiesByRunID(t *testing.T) {
	first := ResolveEffective([]ChainEntry{
		entry(1, 0, "implement", StatusFailed),
		entry(2, 0, "implement", StatusSucceeded),
	})
	second := ResolveEffective([]ChainEntry{
		entry(2, 0, "implement", StatusSucceeded),
		entry(1, 0, "implement", StatusFailed),
	})

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one effective occurrence each, got %+v and %+v", first, second)
	}
	if first[0].RunID != 2 || second[0].RunID != 2 {
		t.Fatalf("expected the higher run ID to win deterministically, got %d and %d", first[0].RunID, second[0].RunID)
	}
}

// Several attempts of one node inside a single run share a run timestamp and
// run ID, so the stable sort must preserve the order Derive emitted them in
// and let the last attempt win.
func TestResolveEffectiveWithinOneRunTakesTheLastAttempt(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "implement", Attempt: 1, Status: StatusErrored}},
		{RunID: 1, RunCreatedAt: at(0), Occurrence: NodeOccurrence{NodeID: "implement", Attempt: 2, Status: StatusSucceeded}},
	})

	if len(effective) != 1 {
		t.Fatalf("expected one effective occurrence, got %+v", effective)
	}
	if effective[0].Status != StatusSucceeded || effective[0].Occurrence.Attempt != 2 {
		t.Fatalf("expected the last attempt to win, got %+v", effective[0])
	}
}

func TestResolveEffectiveUnresolvedFailureNeedsAttention(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{entry(1, 0, "implement", StatusFailed)})
	if len(effective) != 1 || !effective[0].NeedsAttention {
		t.Fatalf("expected an unresolved failure to need attention, got %+v", effective)
	}
}

func TestResolveEffectiveWaitingNeedsAttention(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{entry(1, 0, "approval", StatusWaiting)})
	if len(effective) != 1 || !effective[0].NeedsAttention {
		t.Fatalf("expected a waiting node to need attention, got %+v", effective)
	}
}

// A node the run never reached is no-data, not a call to action. Flagging
// every unreached node would drown the attention view, since Derive projects
// one pending occurrence for every node a run did not get to.
func TestResolveEffectivePendingDoesNotNeedAttention(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{entry(1, 0, "implement", StatusPending)})
	if len(effective) != 1 {
		t.Fatalf("expected the pending occurrence to be reported, got %+v", effective)
	}
	if effective[0].NeedsAttention {
		t.Fatalf("an unreached node is not actionable: %+v", effective[0])
	}
}

func TestResolveEffectiveTerminalAttentionByStatus(t *testing.T) {
	for status, want := range map[Status]bool{
		StatusSucceeded: false,
		StatusSkipped:   false,
		StatusFailed:    true,
		StatusErrored:   true,
		StatusAborted:   true,
	} {
		effective := ResolveEffective([]ChainEntry{entry(1, 0, "implement", status)})
		if len(effective) != 1 {
			t.Fatalf("%q: expected one effective occurrence, got %+v", status, effective)
		}
		if effective[0].NeedsAttention != want {
			t.Fatalf("%q: expected attention %v, got %v", status, want, effective[0].NeedsAttention)
		}
	}
}

// Each node resolves independently: one node's success must not clear another
// node's failure.
func TestResolveEffectiveResolvesEachNodeIndependently(t *testing.T) {
	effective := ResolveEffective([]ChainEntry{
		entry(1, 0, "implement", StatusFailed),
		entry(1, 0, "review", StatusSucceeded),
		entry(2, 5, "review", StatusSucceeded),
	})

	byNode := map[string]Effective{}
	for _, resolved := range effective {
		byNode[resolved.NodeID] = resolved
	}
	if len(byNode) != 2 {
		t.Fatalf("expected both nodes, got %+v", effective)
	}
	if !byNode["implement"].NeedsAttention {
		t.Fatalf("expected the unresolved failure to still need attention, got %+v", byNode["implement"])
	}
	if byNode["review"].NeedsAttention {
		t.Fatalf("expected the succeeded node to be resolved, got %+v", byNode["review"])
	}
}

// No activity at all is no-data, rendered distinctly from success.
func TestResolveEffectiveWithNoEntriesIsEmpty(t *testing.T) {
	if effective := ResolveEffective(nil); len(effective) != 0 {
		t.Fatalf("expected no effective occurrences, got %+v", effective)
	}
}

// Resolution is a read: it must not reorder or otherwise disturb the caller's
// slice, which the caller may still be using for run history.
func TestResolveEffectiveDoesNotMutateItsInput(t *testing.T) {
	entries := []ChainEntry{
		entry(2, 5, "implement", StatusSucceeded),
		entry(1, 0, "implement", StatusFailed),
	}
	ResolveEffective(entries)

	if entries[0].RunID != 2 || entries[1].RunID != 1 {
		t.Fatalf("ResolveEffective reordered its input: %+v", entries)
	}
}

// The full occurrence travels with the resolution so the overview can link
// straight to the wait, publication, or attempt behind it.
func TestResolveEffectiveCarriesTheUnderlyingOccurrence(t *testing.T) {
	waitID := int64(7)
	effective := ResolveEffective([]ChainEntry{{
		RunID:        1,
		RunCreatedAt: at(0),
		Occurrence: NodeOccurrence{
			NodeID: "approval", NodeKind: KindAwait, Status: StatusWaiting, WaitID: &waitID,
		},
	}})

	if len(effective) != 1 {
		t.Fatalf("expected one effective occurrence, got %+v", effective)
	}
	if effective[0].Occurrence.WaitID == nil || *effective[0].Occurrence.WaitID != 7 {
		t.Fatalf("expected the underlying occurrence to be carried, got %+v", effective[0].Occurrence)
	}
	if effective[0].NodeID != "approval" || effective[0].RunID != 1 {
		t.Fatalf("unexpected resolution identity: %+v", effective[0])
	}
}
