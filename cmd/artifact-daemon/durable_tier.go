package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// DurableTier is the daemon's long-term home for resource caches.
//
// Only resource caches go here, and the reason is their keys. A step output is
// addressed by a per-build handle that nobody will ever ask for again, so
// storing one durably buys nothing. A resource cache is addressed by rc-<id>,
// derived from the resource type, version and params, so the same key recurs on
// every build that wants the same thing — including on a node that has never
// seen it.
//
// The node-local copy stays a cache with a TTL. This is the copy that outlives
// the sweeper, the node, and the cluster.
//
// # Nothing here may fail a build
//
// Every method swallows its errors after logging them. That is deliberate and
// it is the property to preserve: a resource cache is re-derivable by re-running
// the get step, so an unreachable bucket must cost a re-download and never a red
// build. Callers get a bool, not an error, so there is no error to accidentally
// propagate.
type DurableTier struct {
	logger  lager.Logger
	store   durable.Store
	metrics *metrics
	timeout time.Duration

	// inFlight collapses concurrent uploads of the same key. Several builds on
	// one node can finish with the same cache at once, and without this each
	// would spool and upload the same bytes.
	mu       sync.Mutex
	inFlight map[string]struct{}
}

// NewDurableTier wraps a store with the daemon's policy.
func NewDurableTier(logger lager.Logger, store durable.Store, m *metrics, timeout time.Duration) *DurableTier {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}

	return &DurableTier{
		logger:   logger.Session("durable"),
		store:    store,
		metrics:  m,
		timeout:  timeout,
		inFlight: map[string]struct{}{},
	}
}

// Has reports whether the durable store holds the key.
//
// A store failure reports false: the caller's next move is to say "not here",
// which is exactly what it would do for a genuine miss.
func (d *DurableTier) Has(ctx context.Context, key string) bool {
	if d == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	_, found, err := d.store.Stat(ctx, key)
	if err != nil {
		d.logger.Error("stat-failed", err, lager.Data{"key": key})
		d.metrics.recordDurable("has", "error")
		return false
	}

	d.metrics.recordDurable("has", outcome(found))

	return found
}

// Restore materialises the durable copy into destDir and reports whether it
// managed to.
//
// It extracts through a temporary sibling and renames, so a failure part-way
// leaves nothing at destDir for the daemon to later serve as a whole artifact.
func (d *DurableTier) Restore(ctx context.Context, key, destDir string) bool {
	if d == nil {
		return false
	}

	logger := d.logger.Session("restore", lager.Data{"key": key})
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	body, found, err := d.store.Get(ctx, key)
	if err != nil {
		logger.Error("get-failed", err)
		d.metrics.recordDurable("restore", "error")
		return false
	}
	if !found {
		d.metrics.recordDurable("restore", "miss")
		return false
	}
	defer body.Close()

	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		logger.Error("create-parent-failed", err)
		d.metrics.recordDurable("restore", "error")
		return false
	}

	tmpDir, err := os.MkdirTemp(filepath.Dir(destDir), ".restore-*")
	if err != nil {
		logger.Error("create-temp-failed", err)
		d.metrics.recordDurable("restore", "error")
		return false
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	if err := extractTarToDir(body, tmpDir); err != nil {
		cleanup()
		logger.Error("extract-failed", err)
		d.metrics.recordDurable("restore", "error")
		return false
	}

	// A previous restore or a racing peer fetch may already have put the
	// artifact here; either copy is equally valid, so keep whichever landed
	// first rather than swapping bytes under an in-flight read.
	if err := os.Rename(tmpDir, destDir); err != nil {
		cleanup()
		if errors.Is(err, os.ErrExist) || isNotEmptyErr(err) {
			logger.Info("already-present")
			d.metrics.recordDurable("restore", "raced")
			return true
		}
		logger.Error("rename-failed", err)
		d.metrics.recordDurable("restore", "error")
		return false
	}

	logger.Info("restored", lager.Data{"duration": time.Since(start).String()})
	d.metrics.recordDurable("restore", "hit")

	return true
}

// Store uploads srcDir under key, tarring it on the way.
//
// It is synchronous; callers that must not block should run it in a goroutine.
// Concurrent calls for the same key collapse to one upload.
func (d *DurableTier) Store(ctx context.Context, key, srcDir string, tar func(io.Writer, string) error) {
	if d == nil {
		return
	}

	if !d.claim(key) {
		return
	}
	defer d.release(key)

	logger := d.logger.Session("store", lager.Data{"key": key})
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	// Tar straight into the upload through a pipe: a resource cache is exactly
	// the kind of artifact that should not be held in memory on every node.
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(tar(pw, srcDir))
	}()
	defer pr.Close()

	if err := d.store.Put(ctx, key, pr); err != nil {
		logger.Error("put-failed", err)
		d.metrics.recordDurable("store", "error")
		return
	}

	logger.Info("stored", lager.Data{"duration": time.Since(start).String()})
	d.metrics.recordDurable("store", "ok")
}

// Delete removes the durable copy. Used by the reclaim path.
func (d *DurableTier) Delete(ctx context.Context, key string) bool {
	if d == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()

	if err := d.store.Delete(ctx, key); err != nil {
		d.logger.Error("delete-failed", err, lager.Data{"key": key})
		d.metrics.recordDurable("delete", "error")
		return false
	}

	d.metrics.recordDurable("delete", "ok")

	return true
}

func (d *DurableTier) claim(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, busy := d.inFlight[key]; busy {
		return false
	}
	d.inFlight[key] = struct{}{}

	return true
}

func (d *DurableTier) release(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.inFlight, key)
}

// resourceCacheKeyPattern matches the keys the ATC mints in
// atc/worker/jetbridge/resource_cache_key.go (ResourceCacheKey: "rc-%d").
//
// Deliberately duplicated rather than imported: the daemon is a separate binary
// and importing atc/worker/jetbridge would drag the Kubernetes client into a
// pod that runs on every node. The two sides agree on a wire format, and this
// is the daemon's half of it.
var resourceCacheKeyPattern = regexp.MustCompile(`^rc-\d+$`)

// isResourceCacheKey reports whether a key names a resource cache, which is the
// only kind of artifact promoted to the durable tier.
func isResourceCacheKey(key string) bool {
	return resourceCacheKeyPattern.MatchString(key)
}

func outcome(found bool) string {
	if found {
		return "hit"
	}

	return "miss"
}

// isNotEmptyErr reports whether err is the "directory not empty" a rename onto
// a populated destination produces. It is checked by errno rather than string
// because the message differs across platforms.
func isNotEmptyErr(err error) bool {
	var linkErr *os.LinkError
	if !errors.As(err, &linkErr) {
		return false
	}

	return errors.Is(linkErr.Err, errDirNotEmpty) || errors.Is(linkErr.Err, os.ErrExist)
}
