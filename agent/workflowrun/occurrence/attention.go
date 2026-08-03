package occurrence

import (
	"sort"
	"time"
)

// ChainEntry is one occurrence located within a retry closure. RunCreatedAt is
// the creating run's timestamp, which supplies the deterministic ordering the
// resolution depends on.
type ChainEntry struct {
	RunID        int64
	RunCreatedAt time.Time
	Occurrence   NodeOccurrence
}

// Effective is the resolved state of one node across a whole retry closure.
type Effective struct {
	NodeID         string
	RunID          int64
	Status         Status
	NeedsAttention bool
	Occurrence     NodeOccurrence
}

// ResolveEffective collapses one retry closure onto the state the overview
// should show. Callers supply the entries of a single closure; the resolution
// buckets them by node identity, which is the (ID, kind) PAIR and not the ID
// alone — see ExecutionNodesOf on why the ID is not unique across kinds. A
// closure can span revisions, so a retired await and the agent that later took
// its name are two nodes here, and resolving them as one would let one node's
// success clear the other's failure.
//
// For each node the effective set is the last terminal occurrence unioned with
// every currently-active occurrence, ordered by (run created at, run ID) so
// branching retries resolve deterministically without inventing causal edges.
//
// Nothing is discarded: the superseded occurrences remain in run history and
// in evaluation statistics. This function answers "what needs action now", not
// "what happened".
func ResolveEffective(entries []ChainEntry) []Effective {
	ordered := append([]ChainEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].RunCreatedAt.Equal(ordered[j].RunCreatedAt) {
			return ordered[i].RunCreatedAt.Before(ordered[j].RunCreatedAt)
		}
		return ordered[i].RunID < ordered[j].RunID
	})

	type bucket struct {
		latestTerminal *ChainEntry
		active         []ChainEntry
	}

	type nodeKey struct {
		id   string
		kind string
	}

	buckets := map[nodeKey]*bucket{}
	var nodeOrder []nodeKey

	for index := range ordered {
		entry := ordered[index]
		key := nodeKey{id: entry.Occurrence.NodeID, kind: entry.Occurrence.NodeKind}
		current, found := buckets[key]
		if !found {
			current = &bucket{}
			buckets[key] = current
			nodeOrder = append(nodeOrder, key)
		}
		if entry.Occurrence.Status.Terminal() {
			copied := entry
			current.latestTerminal = &copied
			continue
		}
		current.active = append(current.active, entry)
	}

	var result []Effective
	for _, key := range nodeOrder {
		current := buckets[key]

		var liveRetry bool
		for _, entry := range current.active {
			attention := activeNeedsAttention(entry.Occurrence.Status)
			liveRetry = liveRetry || attention
			result = append(result, Effective{
				NodeID:         key.id,
				RunID:          entry.RunID,
				Status:         entry.Occurrence.Status,
				NeedsAttention: attention,
				Occurrence:     entry.Occurrence,
			})
		}

		if current.latestTerminal != nil {
			entry := *current.latestTerminal
			// A retry already in flight is addressing the earlier failure, so
			// the failure is not independently actionable. It stays in the
			// effective set — nothing is erased — but the live occurrence is
			// the one that asks for action.
			attention := !liveRetry && terminalNeedsAttention(entry.Occurrence.Status)
			result = append(result, Effective{
				NodeID:         key.id,
				RunID:          entry.RunID,
				Status:         entry.Occurrence.Status,
				NeedsAttention: attention,
				Occurrence:     entry.Occurrence,
			})
		}
	}

	return result
}

// RunsNeedingAttention projects ResolveEffective onto run identity: a run asks
// for action when one of its OWN occurrences is the one the resolution left
// needing attention.
//
// It exists so the run list's server-side attention lens has a single
// definition to be tested against rather than a second, independently authored
// rule. The list answers "which run needs action" and the canvas answers "which
// node needs action"; this is the only place the first is derived from the
// second.
func RunsNeedingAttention(entries []ChainEntry) map[int64]bool {
	needing := map[int64]bool{}
	for _, effective := range ResolveEffective(entries) {
		if effective.NeedsAttention {
			needing[effective.RunID] = true
		}
	}
	return needing
}

// activeNeedsAttention is true for in-flight states a human should be looking
// at. Pending is deliberately excluded: Derive projects one pending occurrence
// for every node a run never reached, and treating no-data as a call to action
// would drown the attention view.
func activeNeedsAttention(status Status) bool {
	switch status {
	case StatusRunning, StatusWaiting:
		return true
	default:
		return false
	}
}

// terminalNeedsAttention is true for terminal states a human must still act
// on. A success or a deliberate skip is resolved.
func terminalNeedsAttention(status Status) bool {
	switch status {
	case StatusFailed, StatusErrored, StatusAborted:
		return true
	default:
		return false
	}
}
