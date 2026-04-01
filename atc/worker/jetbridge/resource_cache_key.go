package jetbridge

import "fmt"

// ResourceCacheKey returns the daemon artifact key for a resource cache.
// This key is registered as an alias on the daemon after a successful get
// step, and probed via HEAD /resource-caches/{key} on subsequent runs to
// check for cache hits.
func ResourceCacheKey(cacheID int) string {
	return fmt.Sprintf("rc-%d", cacheID)
}
