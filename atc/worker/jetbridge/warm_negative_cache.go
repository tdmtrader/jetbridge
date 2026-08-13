package jetbridge

import (
	"sync"
	"time"
)

// warmSuppressionWindow is how long a failed warm silences further attempts for
// the same key.
//
// It is longer than GetResourceLockInterval (5s) on purpose: the point is to
// stop a retry loop that re-enters far faster than a bucket recovers.
const warmSuppressionWindow = 60 * time.Second

// defaultWarmTimeout bounds one durable restore when the operator has not set
// one. Generous relative to a probe, because it covers pulling a whole resource
// cache out of object storage, and still far below the daemon's 2h artifact TTL.
const defaultWarmTimeout = 90 * time.Second

// warmNegativeCache remembers keys whose durable warm just failed.
//
// Without it a degraded bucket is unboundedly expensive rather than merely
// useless. A get step's declared `timeout:` does not bound a warm — MaybeTimeout
// is applied inside performGetAndInitCache, past the cache lookup — and
// attemptGet is re-entered every GetResourceLockInterval while it waits for the
// resource lock. So each 5-second tick would pay the full warm timeout, forever,
// for an answer that has been "no" every time.
//
// It only ever suppresses an optimisation. A suppressed key still runs the get
// step and still produces correct bytes; it just does not ask the bucket again
// for another minute.
type warmNegativeCache struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func newWarmNegativeCache() *warmNegativeCache {
	return &warmNegativeCache{until: map[string]time.Time{}}
}

// suppressed reports whether key is currently silenced, and drops the entry
// once it has expired so the map does not grow with every key ever missed.
func (c *warmNegativeCache) suppressed(key string) bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	deadline, found := c.until[key]
	if !found {
		return false
	}
	if time.Now().Before(deadline) {
		return true
	}
	delete(c.until, key)

	return false
}

func (c *warmNegativeCache) suppress(key string, window time.Duration) {
	if c == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.until[key] = time.Now().Add(window)
}
