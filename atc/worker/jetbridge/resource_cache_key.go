package jetbridge

import (
	"fmt"
	"regexp"

	"github.com/concourse/concourse/atc/db"
)

// ResourceCacheKey returns the daemon artifact key for a resource cache.
// This key is registered as an alias on the daemon after a successful get
// step, and probed via HEAD /resource-caches/{key} on subsequent runs to
// check for cache hits.
//
// It prefers the cache's content key, so a cache promoted to the durable tier
// is stored and retrieved under one name rather than two. `resource_caches.id`
// is a sequence and cannot name anything that outlives the row: see
// durableCacheKey in atc/db/resource_cache.go.
//
// Caches written before the durable_key column have no content key, and fall
// back to the id. That is safe because the fallback is only ever used
// node-locally, where a 2h TTL bounds the blast radius — but it must never
// reach durable storage, which is why RegisterResourceCache sends durable:false
// for exactly these.
func ResourceCacheKey(cache db.ResourceCache) string {
	return resourceCacheKey(cache.ID(), cache.DurableKey())
}

// resourceCacheKey is the choice itself, split out so it can be tested against
// both key shapes without standing up a database or a stub cache.
func resourceCacheKey(id int, durableKey string) string {
	if durableKey != "" {
		return durableKey
	}

	return fmt.Sprintf("rc-%d", id)
}

// DurableClassResourceCache is the retention class every resource cache is
// stored under.
//
// A durable key is "<class>/<identity>", and the class is a prefix an object
// lifecycle rule acts on: the operator decides that resource caches expire after
// N days by writing one rule against this prefix, with no code involved and no
// component that has to be running for the rule to apply.
//
// The class says what an artifact IS, never how long it should live. Duration is
// an operator's policy and belongs in the bucket; encoding it here would mean a
// redeploy to change a retention period, and two sources of truth for what the
// period currently is.
//
// Adding a class is deliberately a code change plus a lifecycle rule. There is
// one today because there is one kind of durable artifact today; a future kind
// (a task cache, a review) gets its own constant and its own rule rather than
// being folded in here.
const DurableClassResourceCache = "resource-caches"

// DurableStorageKey is the name a resource cache is stored under for the long
// term, or empty when it has no content key and is therefore not eligible.
//
// Distinct from ResourceCacheKey, which names the node-local alias. That one
// must stay a single path segment because it becomes a direct child of the
// daemon's steps/ directory, which is the only thing the sweeper reclaims.
func DurableStorageKey(cache db.ResourceCache) string {
	key := cache.DurableKey()
	if key == "" {
		return ""
	}

	return DurableClassResourceCache + "/" + key
}

// resourceCacheKeyPattern matches both key shapes: the legacy id form and the
// content form.
//
// Widening this is load-bearing, and its failure mode is silent. isResourceCacheKey
// gates the re-probe in WrapVolumeForLookup; a pattern that no longer matches
// makes that gate take the non-probing branch, so resource caches simply stop
// being found while every test still passes and no error is logged.
var resourceCacheKeyPattern = regexp.MustCompile(`^rc-([0-9]+|[0-9a-f]{64})$`)

// isResourceCacheKey reports whether the given artifact key refers to a
// resource cache (produced by ResourceCacheKey). Used by WrapVolumeForLookup
// to decide whether to re-probe live daemons when the locator has no entry.
func isResourceCacheKey(key string) bool {
	return resourceCacheKeyPattern.MatchString(key)
}
