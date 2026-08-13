package jetbridge

import (
	"testing"
	"time"
)

// Every ATC process must independently choose the same daemon for a given key.
// If they disagree, N concurrent builds each warm a private copy of the same
// multi-gigabyte cache onto a different node — the exact cost the tier exists to
// avoid.
func TestWarmOwnersIsDeterministicAcrossProcesses(t *testing.T) {
	eps := []daemonEndpoint{
		{IP: "10.0.0.1", Node: "node-a"},
		{IP: "10.0.0.2", Node: "node-b"},
		{IP: "10.0.0.3", Node: "node-c"},
	}
	shuffled := []daemonEndpoint{eps[2], eps[0], eps[1]}

	first := warmOwners("rc-abc", eps)
	second := warmOwners("rc-abc", shuffled)

	if len(first) != len(second) {
		t.Fatalf("ranking lengths differ: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Node != second[i].Node {
			t.Fatalf("ranking depends on input order: %v vs %v", first, second)
		}
	}
}

// Ranking must key on the node name, not the pod IP. A DaemonSet rolling update
// replaces every pod IP at once — the single most common churn event in the
// cluster — and IP-based ranking would reshuffle every key's owner at exactly
// that moment, invalidating every warmed cache simultaneously.
func TestWarmOwnersRanksByNodeNotPodIP(t *testing.T) {
	before := []daemonEndpoint{
		{IP: "10.0.0.1", Node: "node-a"},
		{IP: "10.0.0.2", Node: "node-b"},
		{IP: "10.0.0.3", Node: "node-c"},
	}
	// Same nodes, all-new pod IPs, as after a rolling update.
	after := []daemonEndpoint{
		{IP: "10.1.9.7", Node: "node-a"},
		{IP: "10.1.9.8", Node: "node-b"},
		{IP: "10.1.9.9", Node: "node-c"},
	}

	for _, key := range []string{"rc-abc", "rc-def", "rc-0123456789"} {
		if got, want := warmOwners(key, after)[0].Node, warmOwners(key, before)[0].Node; got != want {
			t.Errorf("key %q moved from %s to %s across a pod-IP roll", key, want, got)
		}
	}
}

// Different keys must not all pile onto one node.
func TestWarmOwnersSpreadsKeysAcrossNodes(t *testing.T) {
	eps := []daemonEndpoint{
		{IP: "10.0.0.1", Node: "node-a"},
		{IP: "10.0.0.2", Node: "node-b"},
		{IP: "10.0.0.3", Node: "node-c"},
	}

	seen := map[string]int{}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		seen[warmOwners(key, eps)[0].Node]++
	}

	if len(seen) < 2 {
		t.Errorf("12 keys landed on %d node(s); ranking is not spreading load: %v", len(seen), seen)
	}
}

// An endpoint with no node name cannot be agreed on by other ATC processes, so
// it is usable but never preferred.
func TestWarmOwnersPrefersEndpointsWithANodeName(t *testing.T) {
	eps := []daemonEndpoint{
		{IP: "10.0.0.1", Node: ""},
		{IP: "10.0.0.2", Node: "node-b"},
	}

	if got := warmOwners("rc-abc", eps)[0].Node; got != "node-b" {
		t.Errorf("first choice was the nameless endpoint (%q); it cannot be agreed on", got)
	}
}

// A failed warm must silence further attempts for a while.
//
// A get step's own timeout does not bound a warm — MaybeTimeout is applied
// further in — and attemptGet re-enters every GetResourceLockInterval while
// waiting for the resource lock. Without suppression, a degraded bucket turns
// each 5-second tick into a full warm timeout, indefinitely.
func TestWarmNegativeCacheSuppressesThenExpires(t *testing.T) {
	c := newWarmNegativeCache()

	if c.suppressed("rc-abc") {
		t.Error("a key nobody has failed reported as suppressed")
	}

	c.suppress("rc-abc", time.Minute)
	if !c.suppressed("rc-abc") {
		t.Error("a just-failed key was not suppressed")
	}
	if c.suppressed("rc-other") {
		t.Error("suppression leaked to an unrelated key")
	}

	c.suppress("rc-expired", -time.Second)
	if c.suppressed("rc-expired") {
		t.Error("an expired suppression still reported as active")
	}
	if _, still := c.until["rc-expired"]; still {
		t.Error("expired entries are not dropped; the map grows with every key ever missed")
	}
}

// The cache is reached through a possibly-nil pointer on backends built without
// one.
func TestNilWarmNegativeCacheIsSafe(t *testing.T) {
	var c *warmNegativeCache

	if c.suppressed("rc-abc") {
		t.Error("nil cache reported suppression")
	}
	c.suppress("rc-abc", time.Minute)
}
