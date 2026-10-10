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
func ResourceCacheKey(cache db.ResourceCache) string {
	return resourceCacheKey(cache.ID(), cache.ContentKey())
}

// resourceCacheKey is the choice itself, split out so it can be tested against
// both key shapes without standing up a database or a stub cache.
func resourceCacheKey(id int, contentKey string) string {
	if contentKey != "" {
		return contentKey
	}

	return fmt.Sprintf("rc-%d", id)
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
