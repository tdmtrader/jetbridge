package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// newDaemon stands up a real server over a real temp directory, optionally with
// a durable tier over a real filesystem store.
func newDaemon(t *testing.T, node string, withTier bool) (*Server, *httptest.Server, durable.Store) {
	t.Helper()

	server := NewServer(lagertest.NewTestLogger("daemon-"+node), t.TempDir(), node)

	var store durable.Store
	if withTier {
		fs, err := durable.NewFS(t.TempDir(), 0)
		if err != nil {
			t.Fatalf("NewFS: %v", err)
		}
		store = fs
		server.SetDurableTier(NewDurableTier(lagertest.NewTestLogger("tier-"+node), store, server.Metrics(), time.Minute))
	}

	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	return server, ts, store
}

func post(t *testing.T, ts *httptest.Server, path, body string) *http.Response {
	t.Helper()

	resp, err := http.Post(ts.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	t.Cleanup(func() { resp.Body.Close() })

	return resp
}

// THE invariant the whole two-phase design exists to protect.
//
// Every daemon shares one bucket. If HEAD answered from the durable store, all
// of them would report 200 for anything ever stored, the ATC's probe would pick
// an arbitrary pod, and the node affinity the probe exists to provide would be
// gone — a cache sitting on node A would be served by node B pulling it back out
// of object storage.
func TestHeadResourceCacheNeverAnswersFromTheDurableStore(t *testing.T) {
	server, ts, store := newDaemon(t, "node-a", true)

	// The object is in the bucket, but nothing is on this node's disk.
	if err := store.Put(context.Background(), "resource-caches/rc-abc", strings.NewReader("tarbytes")); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if !server.durable.Has(context.Background(), "resource-caches/rc-abc") {
		t.Fatal("precondition: the object should be in the durable store")
	}

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/rc-abc", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD answered %d for a cache only in the bucket; a probe hit must mean local bytes", resp.StatusCode)
	}
}

// Capability must be legible on a miss, which is the only time it is consulted.
func TestHeadResourceCacheAdvertisesTheTierOnEveryStatus(t *testing.T) {
	_, ts, _ := newDaemon(t, "node-a", true)

	for _, key := range []string{"rc-absent", "rc-also-absent"} {
		req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/"+key, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("HEAD: %v", err)
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("expected a miss, got %d", resp.StatusCode)
		}
		if resp.Header.Get(DurableTierHeader) == "" {
			t.Error("a 404 carried no durable-tier header; the ATC learns capability only from misses")
		}
	}
}

// A daemon without a tier must look exactly like one that predates it, so an
// upgraded ATC makes zero requests to a route it cannot serve.
func TestHeadResourceCacheAdvertisesNothingWithoutATier(t *testing.T) {
	_, ts, _ := newDaemon(t, "node-a", false)

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/rc-1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get(DurableTierHeader); got != "" {
		t.Errorf("a daemon with no durable tier advertised %q", got)
	}
}

func TestDurableRestoreMaterialisesAndRegisters(t *testing.T) {
	server, ts, store := newDaemon(t, "node-a", true)

	// Seed the store with a real tar of a real directory, produced the same way
	// a promotion would produce it.
	src := writeDir(t, t.TempDir(), "payload", map[string]string{"file": "cached bytes"})
	var buf bytes.Buffer
	if err := server.tarDirectory(&buf, src); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := store.Put(context.Background(), "resource-caches/rc-abc", &buf); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	resp := post(t, ts, "/durable/restore", `{"key":"rc-abc","durable_key":"resource-caches/rc-abc"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("restore returned %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get(ArtifactTierHeader); got != "durable" {
		t.Errorf("tier header = %q, want durable", got)
	}

	// The bytes landed where the sweeper reclaims them: a direct child of steps/.
	dest := filepath.Join(server.storagePath, "steps", "rc-abc")
	got, err := os.ReadFile(filepath.Join(dest, "file"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(got) != "cached bytes" {
		t.Errorf("restored %q", got)
	}

	// ...and the alias exists, so a subsequent probe is a local hit. This is
	// what makes the restore self-fulfilling: the caller may bind to this pod.
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/rc-abc", nil)
	head, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD after restore: %v", err)
	}
	defer head.Body.Close()

	if head.StatusCode != http.StatusOK {
		t.Errorf("after a restore the cache is still not local (HEAD %d)", head.StatusCode)
	}
}

func TestDurableRestoreOfAnAbsentObjectIs404(t *testing.T) {
	_, ts, _ := newDaemon(t, "node-a", true)

	if resp := post(t, ts, "/durable/restore", `{"key":"rc-nothing","durable_key":"resource-caches/rc-nothing"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("restore of an absent object returned %d, want 404", resp.StatusCode)
	}
}

// 501, not 404: a cluster that was never configured must not read as one whose
// cache is merely cold.
func TestDurableRestoreWithoutATierIs501(t *testing.T) {
	_, ts, _ := newDaemon(t, "node-a", false)

	if resp := post(t, ts, "/durable/restore", `{"key":"rc-abc","durable_key":"resource-caches/rc-abc"}`); resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("restore with no tier returned %d, want 501", resp.StatusCode)
	}
}

// The key becomes a path under the storage root. In the path it would have to be
// un-escaped first, and "%2e%2e%2f" decodes to "../" — which is why it travels
// in the body and is validated as a single segment.
func TestDurableRestoreRejectsKeysThatEscapeTheStorageRoot(t *testing.T) {
	server, ts, _ := newDaemon(t, "node-a", true)

	for _, key := range []string{
		"../../etc", "..", ".", "a/b/c", "/abs", "", "-leading-dash", "with space", "a/../b",
	} {
		body, _ := json.Marshal(durableRestoreRequest{Key: "rc-abc", DurableKey: key})
		resp := post(t, ts, "/durable/restore", string(body))

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("key %q returned %d, want 400", key, resp.StatusCode)
		}
	}

	// Nothing was created anywhere near the storage root.
	if entries, err := os.ReadDir(server.storagePath); err == nil {
		for _, e := range entries {
			if e.Name() != "steps" {
				t.Errorf("a rejected key created %q under the storage root", e.Name())
			}
		}
	}
}

// Several builds on one node wanting the same cache must produce one download,
// not one each.
func TestConcurrentRestoresOfOneKeyCollapse(t *testing.T) {
	const concurrentRestores = 6
	const key = "rc-abc"
	const durableKey = "resource-caches/rc-abc"

	protocol := newS3ProtocolState(t)
	store := protocol.store(t)
	server := NewServer(lagertest.NewTestLogger("daemon-node-a"), t.TempDir(), "node-a")
	server.SetDurableTier(NewDurableTier(lagertest.NewTestLogger("tier"), store, server.Metrics(), time.Minute))

	src := writeDir(t, t.TempDir(), "payload", map[string]string{"file": "x"})
	var buf bytes.Buffer
	if err := server.tarDirectory(&buf, src); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := store.Put(context.Background(), durableKey, &buf); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Admit every public restore request before any of them can call the real
	// handler. The first S3 GET is then held below, giving every handler a
	// deterministic overlapping request to join instead of relying on a sleep.
	arrived := make(chan struct{}, concurrentRestores)
	admit := make(chan struct{})
	var admitOnce sync.Once
	openAdmit := func() { admitOnce.Do(func() { close(admit) }) }
	handler := server.Handler()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/durable/restore" {
			arrived <- struct{}{}
			<-admit
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(openAdmit)

	// Register this cleanup after the daemon server's cleanup so a failed spec
	// always releases the backend before httptest waits for handlers to exit.
	transfer := protocol.gateTransfer(t, http.MethodGet, durableKey)

	type restoreResult struct {
		status int
		err    error
	}
	ready := make(chan struct{}, concurrentRestores)
	start := make(chan struct{})
	results := make(chan restoreResult, concurrentRestores)
	for range concurrentRestores {
		go func() {
			ready <- struct{}{}
			<-start
			resp, err := http.Post(ts.URL+"/durable/restore", "application/json", strings.NewReader(`{"key":"rc-abc","durable_key":"resource-caches/rc-abc"}`))
			if err != nil {
				results <- restoreResult{err: err}
				return
			}
			resp.Body.Close()
			results <- restoreResult{status: resp.StatusCode}
		}()
	}
	for range concurrentRestores {
		<-ready
	}
	close(start)
	for range concurrentRestores {
		select {
		case <-arrived:
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent restore did not reach the public HTTP boundary")
		}
	}
	openAdmit()

	transfer.waitUntilEntered(t)
	if got := len(protocol.requestsFor(http.MethodGet, durableKey)); got != 1 {
		t.Fatalf("while the first restore was held, the S3 protocol saw %d downloads, want exactly 1", got)
	}
	select {
	case result := <-results:
		t.Fatalf("restore completed before its S3 transfer was released: %+v", result)
	default:
	}

	transfer.open()
	for range concurrentRestores {
		select {
		case result := <-results:
			if result.err != nil {
				t.Errorf("restore request failed: %v", result.err)
			} else if result.status != http.StatusCreated {
				t.Errorf("overlapping restore returned %d, want %d", result.status, http.StatusCreated)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a concurrent restore did not finish after the S3 transfer was released")
		}
	}

	if got := len(protocol.requestsFor(http.MethodGet, durableKey)); got != 1 {
		t.Errorf("%d overlapping restores produced %d S3 downloads, want exactly 1", concurrentRestores, got)
	}
}

// Registration is the ATC's eligibility signal, and it is a bool. A register
// without it must upload nothing, which is what keeps step outputs — and caches
// with no content key — out of permanent storage.
func TestRegisterPromotesOnlyWhenTheATCNamesADurableKey(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		wantUpload bool
	}{
		{"durable key given", `{"key":%q,"local_path":%q,"durable_key":"resource-caches/rc-abc"}`, true},
		{"durable key empty", `{"key":%q,"local_path":%q,"durable_key":""}`, false},
		{"field absent", `{"key":%q,"local_path":%q}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, ts, store := newDaemon(t, "node-a", true)
			src := writeDir(t, t.TempDir(), "payload", map[string]string{"file": "x"})

			resp := post(t, ts, "/register", fmt.Sprintf(tc.body, "rc-abc", src))
			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("register returned %d", resp.StatusCode)
			}

			if uploaded := eventuallyHas(store, "resource-caches/rc-abc", 2*time.Second); uploaded != tc.wantUpload {
				t.Errorf("uploaded = %v, want %v", uploaded, tc.wantUpload)
			}
			_ = server
		})
	}
}

// eventuallyHas polls, because promotion is detached from the response on
// purpose: the build's next step must not wait on an upload.
func eventuallyHas(store durable.Store, key string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, found, _ := store.Stat(context.Background(), key); found {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}

	return false
}

// The two names are different namespaces and a restore must keep them apart.
//
// The object lives in the bucket under a retention-class prefix; the local copy
// must land as a DIRECT child of steps/, because that is the only thing the
// sweeper reclaims. Using the durable key for the destination would nest it one
// level down, where it would never be swept and node disk would grow without
// bound — the way task caches on hostPath already did once.
func TestRestoreLandsFlatEvenWhenTheDurableKeyIsPrefixed(t *testing.T) {
	server, ts, store := newDaemon(t, "node-a", true)

	src := writeDir(t, t.TempDir(), "payload", map[string]string{"file": "x"})
	var buf bytes.Buffer
	if err := server.tarDirectory(&buf, src); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := store.Put(context.Background(), "resource-caches/rc-abc", &buf); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := post(t, ts, "/durable/restore", `{"key":"rc-abc","durable_key":"resource-caches/rc-abc"}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("restore returned %d", resp.StatusCode)
	}

	steps := filepath.Join(server.storagePath, "steps")
	entries, err := os.ReadDir(steps)
	if err != nil {
		t.Fatalf("read steps/: %v", err)
	}

	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "rc-abc" {
		t.Fatalf("steps/ contains %v, want exactly [rc-abc] — a nested restore is never swept", names)
	}
	if _, err := os.Stat(filepath.Join(steps, "rc-abc", "file")); err != nil {
		t.Errorf("restored payload not at steps/rc-abc: %v", err)
	}
}

// A local alias with a slash would nest under steps/ and escape the sweeper, so
// it is rejected even though the same string is a legal durable key.
func TestRestoreRejectsANestedLocalKey(t *testing.T) {
	_, ts, _ := newDaemon(t, "node-a", true)

	resp := post(t, ts, "/durable/restore",
		`{"key":"resource-caches/rc-abc","durable_key":"resource-caches/rc-abc"}`)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("a nested local key returned %d, want 400", resp.StatusCode)
	}
}

// Register stores under the name the ATC gave, not under the local alias.
// Conflating them would drop the retention class, and the object would then fall
// outside every lifecycle rule the operator wrote.
func TestRegisterStoresUnderTheDurableKeyNotTheAlias(t *testing.T) {
	_, ts, store := newDaemon(t, "node-a", true)
	src := writeDir(t, t.TempDir(), "payload", map[string]string{"file": "x"})

	resp := post(t, ts, "/register",
		fmt.Sprintf(`{"key":"rc-abc","local_path":%q,"durable_key":"resource-caches/rc-abc"}`, src))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register returned %d", resp.StatusCode)
	}

	if !eventuallyHas(store, "resource-caches/rc-abc", 2*time.Second) {
		t.Error("object was not stored under the class-prefixed durable key")
	}
	if _, found, _ := store.Stat(context.Background(), "rc-abc"); found {
		t.Error("object was stored under the bare local alias; the retention class is lost")
	}
}
