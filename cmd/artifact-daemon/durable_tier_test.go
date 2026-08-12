package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// newTierServer wires a Server to a filesystem-backed durable tier and returns
// both, plus the storage root. The real Server and the real store — the only
// substitution is the backend, and that is a real implementation too.
func newTierServer(t *testing.T) (*Server, *Server, string, string) {
	t.Helper()

	storage := t.TempDir()
	bucket := t.TempDir()

	store, err := durable.NewFS(bucket, 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	logger := lagertest.NewTestLogger("durable-test")

	producer := NewServer(logger, storage, "node-a")
	producer.SetDurableTier(NewDurableTier(logger, store, producer.Metrics(), time.Minute))

	// A second server on its own storage root, sharing the bucket: this is the
	// case the feature exists for — a node that has never seen the artifact.
	coldStorage := t.TempDir()
	consumer := NewServer(logger, coldStorage, "node-b")
	consumer.SetDurableTier(NewDurableTier(logger, store, consumer.Metrics(), time.Minute))

	return producer, consumer, storage, coldStorage
}

func writeCache(t *testing.T, storage, key string, files map[string]string) string {
	t.Helper()

	dir := filepath.Join(storage, "steps", key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	return dir
}

// register posts to /register and waits for the detached upload to land.
func register(t *testing.T, s *Server, key, path string) {
	t.Helper()

	body, _ := json.Marshal(registerRequest{Key: key, LocalPath: path})
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	s.handleRegister(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201", rec.Code)
	}
}

// waitForDurable polls until the tier reports the key, because the upload is
// deliberately detached from the register response.
func waitForDurable(t *testing.T, s *Server, key string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.durable.Has(context.Background(), key) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("artifact %q never reached the durable store", key)
}

func TestRegisteringAResourceCacheUploadsIt(t *testing.T) {
	producer, _, storage, _ := newTierServer(t)

	dir := writeCache(t, storage, "rc-1", map[string]string{"payload": "cached bytes"})
	register(t, producer, "rc-1", dir)

	waitForDurable(t, producer, "rc-1")
}

func TestRegisteringAStepOutputDoesNotUploadIt(t *testing.T) {
	// Step outputs are addressed by a per-build handle. Storing one durably
	// buys nothing and costs a bucket, so the tier must ignore them.
	producer, _, storage, _ := newTierServer(t)

	handle := "8f14e45fceea167a5a36dedd4bea2543"
	dir := writeCache(t, storage, handle, map[string]string{"payload": "step output"})
	register(t, producer, handle, dir)

	// Give a mistaken upload time to happen.
	time.Sleep(300 * time.Millisecond)

	if producer.durable.Has(context.Background(), handle) {
		t.Fatal("a step output was promoted to the durable store")
	}
}

func TestAColdNodeRestoresFromDurableAndServesIt(t *testing.T) {
	// The whole point: node-b has never seen this cache and its own storage is
	// empty, but the artifact survives because the durable tier holds it.
	producer, consumer, storage, coldStorage := newTierServer(t)

	dir := writeCache(t, storage, "rc-7", map[string]string{"payload": "shared cache"})
	register(t, producer, "rc-7", dir)
	waitForDurable(t, producer, "rc-7")

	req := httptest.NewRequest(http.MethodGet, "/resource-caches/rc-7", nil)
	rec := httptest.NewRecorder()
	consumer.handleGetResourceCache(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cold-node GET = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-tar" {
		t.Fatalf("Content-Type = %q, want application/x-tar", ct)
	}
	if !strings.Contains(rec.Body.String(), "shared cache") {
		t.Fatal("restored tar does not contain the cached bytes")
	}

	// The restore lands under steps/ deliberately, so the existing sweeper
	// reclaims it at TTL. A warmed copy nothing reclaims fills the node disk.
	restored := filepath.Join(coldStorage, "steps", "rc-7")
	if _, err := os.Stat(restored); err != nil {
		t.Fatalf("restore did not land under steps/: %v", err)
	}
}

func TestHeadOnAColdNodeReportsADurableHit(t *testing.T) {
	producer, consumer, storage, _ := newTierServer(t)

	dir := writeCache(t, storage, "rc-8", map[string]string{"payload": "x"})
	register(t, producer, "rc-8", dir)
	waitForDurable(t, producer, "rc-8")

	req := httptest.NewRequest(http.MethodHead, "/resource-caches/rc-8", nil)
	rec := httptest.NewRecorder()
	consumer.handleHeadResourceCache(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("cold-node HEAD = %d, want 200", rec.Code)
	}
	if src := rec.Header().Get("X-Artifact-Source"); src != "durable" {
		t.Fatalf("X-Artifact-Source = %q, want durable", src)
	}
}

func TestAMissStillLooksLikeAMiss(t *testing.T) {
	_, consumer, _, _ := newTierServer(t)

	for _, tc := range []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
	}{
		{"HEAD", consumer.handleHeadResourceCache, http.MethodHead},
		{"GET", consumer.handleGetResourceCache, http.MethodGet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/resource-caches/rc-999", nil)
			rec := httptest.NewRecorder()
			tc.handler(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s of an absent key = %d, want 404", tc.name, rec.Code)
			}
		})
	}
}

// brokenStore fails every operation, standing in for an unreachable bucket,
// an expired credential, or a bad endpoint.
type brokenStore struct{}

func (brokenStore) Has(context.Context, string) (bool, error) {
	return false, errors.New("bucket unreachable")
}
func (brokenStore) Get(context.Context, string) (io.ReadCloser, bool, error) {
	return nil, false, errors.New("bucket unreachable")
}
func (brokenStore) Put(context.Context, string, io.Reader) error {
	return errors.New("bucket unreachable")
}
func (brokenStore) Delete(context.Context, string) error {
	return errors.New("bucket unreachable")
}

func TestABrokenStoreDegradesInsteadOfFailingTheBuild(t *testing.T) {
	// This is the property the whole design rests on. A resource cache is
	// re-derivable, so an unreachable bucket must cost a re-download and
	// nothing else. A 500 here becomes a red build for a cache miss.
	logger := lagertest.NewTestLogger("broken")
	storage := t.TempDir()

	server := NewServer(logger, storage, "node-a")
	server.SetDurableTier(NewDurableTier(logger, brokenStore{}, server.Metrics(), time.Second))

	t.Run("HEAD answers 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/resource-caches/rc-1", nil)
		rec := httptest.NewRecorder()
		server.handleHeadResourceCache(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("HEAD against a broken store = %d, want 404", rec.Code)
		}
	})

	t.Run("GET answers 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/resource-caches/rc-1", nil)
		rec := httptest.NewRecorder()
		server.handleGetResourceCache(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET against a broken store = %d, want 404", rec.Code)
		}
	})

	t.Run("register still succeeds", func(t *testing.T) {
		// The ATC is waiting on this response. A failed upload must not fail
		// the registration that the build's next step depends on.
		dir := writeCache(t, storage, "rc-2", map[string]string{"payload": "x"})
		register(t, server, "rc-2", dir)
	})
}

func TestNoDurableTierBehavesExactlyAsBefore(t *testing.T) {
	// The feature is off by default, and off must mean untouched.
	logger := lagertest.NewTestLogger("no-tier")
	storage := t.TempDir()
	server := NewServer(logger, storage, "node-a")

	req := httptest.NewRequest(http.MethodGet, "/resource-caches/rc-1", nil)
	rec := httptest.NewRecorder()
	server.handleGetResourceCache(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET with no durable tier = %d, want 404", rec.Code)
	}

	dir := writeCache(t, storage, "rc-1", map[string]string{"payload": "x"})
	register(t, server, "rc-1", dir)
}

func TestConcurrentUploadsOfOneKeyCollapse(t *testing.T) {
	logger := lagertest.NewTestLogger("collapse")
	storage := t.TempDir()
	counting := &countingStore{inner: mustFS(t)}

	server := NewServer(logger, storage, "node-a")
	tier := NewDurableTier(logger, counting, server.Metrics(), time.Minute)

	dir := writeCache(t, storage, "rc-5", map[string]string{"payload": "x"})

	done := make(chan struct{})
	for range 5 {
		go func() {
			defer func() { done <- struct{}{} }()
			tier.Store(context.Background(), "rc-5", dir, server.tarDirectory)
		}()
	}
	for range 5 {
		<-done
	}

	if got := counting.puts.Load(); got > 1 {
		t.Fatalf("%d concurrent Stores produced %d uploads, want 1", 5, got)
	}
}

func mustFS(t *testing.T) durable.Store {
	t.Helper()
	store, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	return store
}

// countingStore counts Puts so a test can prove concurrent uploads of one key
// collapse to a single transfer.
type countingStore struct {
	inner durable.Store
	puts  atomic.Int64
}

func (c *countingStore) Has(ctx context.Context, key string) (bool, error) {
	return c.inner.Has(ctx, key)
}
func (c *countingStore) Get(ctx context.Context, key string) (io.ReadCloser, bool, error) {
	return c.inner.Get(ctx, key)
}
func (c *countingStore) Put(ctx context.Context, key string, body io.Reader) error {
	c.puts.Add(1)
	// Slow enough that the other callers are genuinely concurrent.
	time.Sleep(50 * time.Millisecond)

	return c.inner.Put(ctx, key, body)
}
func (c *countingStore) Delete(ctx context.Context, key string) error {
	return c.inner.Delete(ctx, key)
}
