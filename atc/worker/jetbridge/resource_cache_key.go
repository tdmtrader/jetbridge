package jetbridge

import (
	"fmt"
	"regexp"
)

// ResourceCacheKey returns the daemon artifact key for a resource cache.
// This key is registered as an alias on the daemon after a successful get
// step, and probed via HEAD /resource-caches/{key} on subsequent runs to
// check for cache hits.
func ResourceCacheKey(cacheID int) string {
	return fmt.Sprintf("rc-%d", cacheID)
}

var resourceCacheKeyPattern = regexp.MustCompile(`^rc-\d+$`)

// isResourceCacheKey reports whether the given artifact key refers to a
// resource cache (produced by ResourceCacheKey). Used by WrapVolumeForLookup
// to decide whether to re-probe live daemons when the locator has no entry.
func isResourceCacheKey(key string) bool {
	return resourceCacheKeyPattern.MatchString(key)
}
