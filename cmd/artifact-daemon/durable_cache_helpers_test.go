package main

import (
	"testing"
	"time"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
	"github.com/concourse/concourse/hangar/gcstest"
)

const testCacheBucket = "caches"

// newTestCache is the cache tier's real wrapper over the in-memory object
// client, which is also its delete client. The backends themselves are
// exercised in the durable package against fake-gcs-server and a real disk
// store; what is under test here is what the daemon does with the answers.
func newTestCache(t *testing.T) (durable.Store, error) {
	store, _ := newSeedableCache(t)
	return store, nil
}

// newSeedableCache also returns the object client, so a test can put an object
// at a chosen age.
func newSeedableCache(t *testing.T) (durable.Store, *gcstest.Memory) {
	t.Helper()
	memory := gcstest.NewMemory()
	memory.CreateBucket(testCacheBucket)
	return durable.New(memory, memory, testCacheBucket, 0), memory
}

// seed writes an object and back-dates it, so a test can express age directly
// rather than sleeping.
func seed(t *testing.T, memory *gcstest.Memory, key string, body string, age time.Duration) {
	t.Helper()
	memory.SeedCreatedAt(testCacheBucket, key, []byte(body), time.Now().Add(-age))
}
