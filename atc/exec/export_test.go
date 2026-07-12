package exec

// Test-only exports (external test package atc/exec_test).

// NewAgentCostObserver exposes the server-side stdout cost observer so its
// stream-parsing semantics (chunking, overflow, per-field max floor) can be
// exercised directly.
var NewAgentCostObserver = newAgentCostObserver

// ObservedAgentCost aliases the observer's result type for assertions.
type ObservedAgentCost = observedAgentCost
