package durable_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// Both backends are exercised through the same table. A store that only works
// on disk is not the feature; the contract is what the daemon depends on.
func eachStore(t *testing.T, run func(t *testing.T, store durable.Store)) {
	t.Helper()

	t.Run("fs", func(t *testing.T) {
		store, err := durable.NewFS(t.TempDir(), 0)
		if err != nil {
			t.Fatalf("NewFS: %v", err)
		}
		run(t, store)
	})

	t.Run("s3", func(t *testing.T) {
		srv := newFakeS3(t)
		run(t, srv.store(t, 0))
	})

	t.Run("gcs", func(t *testing.T) {
		srv := newFakeGCS(t)
		run(t, srv.store(t, 0))
	})
}

func TestGetOfAbsentKeyIsAMissNotAnError(t *testing.T) {
	// The whole tier is fail-open, and this is where that starts: a caller
	// must be able to tell "not stored" from "store is broken" without
	// parsing an error.
	eachStore(t, func(t *testing.T, store durable.Store) {
		body, found, err := store.Get(context.Background(), "rc-404")
		if err != nil {
			t.Fatalf("Get of absent key returned an error: %v", err)
		}
		if found {
			t.Fatal("Get reported a hit for a key that was never written")
		}
		if body != nil {
			t.Fatal("Get returned a body alongside a miss")
		}
	})
}

func TestStatOfAbsentKeyIsFalseNotAnError(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		_, found, err := store.Stat(context.Background(), "rc-404")
		if err != nil {
			t.Fatalf("Stat of absent key returned an error: %v", err)
		}
		if found {
			t.Fatal("Stat reported a hit for a key that was never written")
		}
	})
}

func TestPutThenGetRoundTrips(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		want := []byte("resource cache tar bytes")

		if err := store.Put(ctx, "rc-1", bytes.NewReader(want)); err != nil {
			t.Fatalf("Put: %v", err)
		}

		attrs, found, err := store.Stat(ctx, "rc-1")
		if err != nil || !found {
			t.Fatalf("Stat after Put = (%v, %v), want (true, nil)", found, err)
		}
		if attrs.Size != int64(len(want)) {
			t.Fatalf("Stat reported %d bytes, want %d", attrs.Size, len(want))
		}

		body, found, err := store.Get(ctx, "rc-1")
		if err != nil || !found {
			t.Fatalf("Get after Put = (%v, %v), want (true, nil)", found, err)
		}
		defer body.Close()

		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round-tripped %q, want %q", got, want)
		}
	})
}

func TestPutReplacesAnExistingObject(t *testing.T) {
	// Keys are content-derived upstream, so a rewrite is expected and must not
	// leave the old bytes behind.
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()

		if err := store.Put(ctx, "rc-2", strings.NewReader("first")); err != nil {
			t.Fatalf("first Put: %v", err)
		}
		if err := store.Put(ctx, "rc-2", strings.NewReader("second")); err != nil {
			t.Fatalf("second Put: %v", err)
		}

		body, _, err := store.Get(ctx, "rc-2")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer body.Close()

		got, _ := io.ReadAll(body)
		if string(got) != "second" {
			t.Fatalf("got %q, want the second write", got)
		}
	})
}

func TestDeleteIsIdempotent(t *testing.T) {
	// A reclaim pass has no memory of what it already removed, so deleting an
	// absent key has to succeed or every second pass fails.
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()

		if err := store.Put(ctx, "rc-3", strings.NewReader("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.Delete(ctx, "rc-3"); err != nil {
			t.Fatalf("first Delete: %v", err)
		}
		if err := store.Delete(ctx, "rc-3"); err != nil {
			t.Fatalf("Delete of an already-deleted key: %v", err)
		}

		if _, found, _ := store.Stat(ctx, "rc-3"); found {
			t.Fatal("key still present after Delete")
		}
	})
}

func TestKeysThatCouldEscapeTheStoreAreRejected(t *testing.T) {
	// The fs backend joins the key onto a root directory, so a key that walks
	// out of it writes anywhere the daemon can reach -- and the daemon runs as
	// root with CAP_DAC_OVERRIDE.
	//
	// One slash is legal (a retention-class prefix; see TestClassPrefixedKeys).
	// Everything here is not.
	escapes := []string{
		"../etc/passwd",
		"a/b/c",  // two levels: a lifecycle rule matches a prefix, so depth is not free
		"a/../b", // one slash after cleaning, but three segments before it
		"a/..",   // trailing traversal
		"../b",   // leading traversal
		"a/",     // empty second segment
		"/b",     // empty first segment
		"",
		".",
		"..",
		"/absolute",
		strings.Repeat("x", 256),
		"toolong/" + strings.Repeat("x", 256),
	}

	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		for _, key := range escapes {
			if err := store.Put(ctx, key, strings.NewReader("x")); err == nil {
				t.Errorf("Put(%q) was accepted; it must be rejected", key)
			}
			if _, _, err := store.Get(ctx, key); err == nil {
				t.Errorf("Get(%q) was accepted; it must be rejected", key)
			}
			if _, _, err := store.Stat(ctx, key); err == nil {
				t.Errorf("Stat(%q) was accepted; it must be rejected", key)
			}
			if err := store.Delete(ctx, key); err == nil {
				t.Errorf("Delete(%q) was accepted; it must be rejected", key)
			}
		}
	})
}

func TestPutOverTheLimitFailsRatherThanTruncating(t *testing.T) {
	// A truncated object restores as a short-but-valid tar, and the build that
	// consumes it fails somewhere far away from the cause.
	t.Run("fs", func(t *testing.T) {
		store, err := durable.NewFS(t.TempDir(), 8)
		if err != nil {
			t.Fatalf("NewFS: %v", err)
		}

		err = store.Put(context.Background(), "rc-big", strings.NewReader("more than eight bytes"))
		if !errors.Is(err, durable.ErrTooLarge) {
			t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
		}

		if _, found, _ := store.Stat(context.Background(), "rc-big"); found {
			t.Fatal("an over-limit Put left an object behind")
		}
	})

	t.Run("s3", func(t *testing.T) {
		srv := newFakeS3(t)
		store := srv.store(t, 8)

		err := store.Put(context.Background(), "rc-big", strings.NewReader("more than eight bytes"))
		if !errors.Is(err, durable.ErrTooLarge) {
			t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
		}
		if srv.has("rc-big") {
			t.Fatal("an over-limit Put reached the bucket")
		}
	})

	t.Run("gcs", func(t *testing.T) {
		// Cloud Storage's writer finalises on Close, so a truncated body has
		// to cancel the context instead. Without that this publishes a short
		// object and reports success.
		srv := newFakeGCS(t)
		store := srv.store(t, 8)

		err := store.Put(context.Background(), "rc-big", strings.NewReader("more than eight bytes"))
		if !errors.Is(err, durable.ErrTooLarge) {
			t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
		}
		if srv.has("rc-big") {
			t.Fatal("an over-limit Put reached the bucket")
		}
	})
}

func TestPutAtExactlyTheLimitSucceeds(t *testing.T) {
	store, err := durable.NewFS(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if err := store.Put(context.Background(), "rc-exact", strings.NewReader("12345")); err != nil {
		t.Fatalf("Put of exactly the limit: %v", err)
	}
}

func TestFSPutLeavesNoPartialFileWhenTheBodyFails(t *testing.T) {
	// A reader that dies mid-copy must not leave a file a later Get would
	// serve as whole.
	dir := t.TempDir()
	store, err := durable.NewFS(dir, 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	err = store.Put(context.Background(), "rc-partial", io.MultiReader(
		strings.NewReader("some bytes"),
		errReader{errors.New("connection reset")},
	))
	if err == nil {
		t.Fatal("Put with a failing body succeeded")
	}

	if _, found, _ := store.Stat(context.Background(), "rc-partial"); found {
		t.Fatal("a failed Put left the object visible")
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".put-") {
			continue
		}
		t.Fatalf("a failed Put left a temp file behind: %s", e.Name())
	}
}

func TestS3UsesThePrefixForEveryOperation(t *testing.T) {
	// A prefix that applies to Put but not Delete leaves objects nothing will
	// ever reclaim.
	srv := newFakeS3(t)
	store, err := durable.NewS3(context.Background(), durable.S3Config{
		Bucket:          "artifacts",
		Prefix:          "jetbridge/prod",
		Endpoint:        srv.URL(),
		Region:          "us-east-1",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	ctx := context.Background()
	if err := store.Put(ctx, "rc-9", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !srv.has("jetbridge/prod/rc-9") {
		t.Fatalf("object not stored under the prefix; keys present: %v", srv.keys())
	}

	if _, found, err := store.Stat(ctx, "rc-9"); err != nil || !found {
		t.Fatalf("Stat = (%v, %v), want (true, nil)", found, err)
	}
	if err := store.Delete(ctx, "rc-9"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if srv.has("jetbridge/prod/rc-9") {
		t.Fatal("Delete did not remove the prefixed object")
	}
}

func TestS3ServerErrorsSurfaceAsErrorsNotMisses(t *testing.T) {
	// Fail-open is the caller's job, and it can only do it if the store tells
	// the truth: a 500 is not "absent".
	srv := newFakeS3(t)
	srv.fail(http.StatusInternalServerError)
	store := srv.store(t, 0)

	if _, _, err := store.Get(context.Background(), "rc-1"); err == nil {
		t.Fatal("Get against a failing bucket reported success")
	}
	if _, _, err := store.Stat(context.Background(), "rc-1"); err == nil {
		t.Fatal("Stat against a failing bucket reported success")
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// fakeS3 is a minimal S3-compatible object endpoint. It is a real HTTP server
// speaking the real wire protocol to the real SDK — the repo's convention is
// that a fake of the library under test proves nothing.
type fakeS3 struct {
	srv        *httptest.Server
	mu         sync.Mutex
	objects    map[string][]byte
	failStatus int
	seen       int
}

func newFakeS3(t *testing.T) *fakeS3 {
	t.Helper()

	f := &fakeS3{objects: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeS3) URL() string { return f.srv.URL }

// requests reports how many HTTP requests the SDK actually made, which is how
// the retry cap is observed.
func (f *fakeS3) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.seen
}

func (f *fakeS3) fail(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStatus = status
}

func (f *fakeS3) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]

	return ok
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}

	return out
}

func (f *fakeS3) store(t *testing.T, limit int64) durable.Store {
	t.Helper()

	store, err := durable.NewS3(context.Background(), durable.S3Config{
		Bucket:          "artifacts",
		Endpoint:        f.URL(),
		Region:          "us-east-1",
		AccessKeyID:     "test",
		SecretAccessKey: "test",
		Limit:           limit,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	return store
}

// serve implements just enough of S3: path-style addressing, so the URL is
// /<bucket>/<key...>.
func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen++
	failStatus := f.failStatus
	f.mu.Unlock()

	if failStatus != 0 {
		http.Error(w, "injected failure", failStatus)
		return
	}

	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 2)

	// A bucket-level GET with list-type=2 is ListObjectsV2, not an object
	// fetch: the key is empty and the query carries the request.
	if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
		f.listObjects(w, r)
		return
	}

	if len(parts) != 2 || parts[1] == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	key := parts[1]

	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		f.mu.Lock()
		f.objects[key] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case http.MethodHead:
		f.mu.Lock()
		body, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			// S3 answers a HEAD miss with a bare 404 and no body, which the
			// SDK surfaces as types.NotFound rather than NoSuchKey.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)

	case http.MethodGet:
		f.mu.Lock()
		body, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`<?xml version="1.0"?><Error><Code>NoSuchKey</Code>` +
				`<Message>The specified key does not exist.</Message></Error>`))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)

	case http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unsupported", http.StatusMethodNotAllowed)
	}
}

func TestS3StopsRetryingAFailingBucket(t *testing.T) {
	// Fail-open only helps if it is fast. The tier above degrades to a cache
	// miss on error, so every retry is time a build spends waiting for an
	// answer that will be ignored.
	//
	// Asserted by counting requests rather than by timing: a wall-clock bound
	// loose enough not to flake on a loaded machine is also loose enough to
	// pass with the cap removed, which is exactly what the first version of
	// this test did.
	srv := newFakeS3(t)
	srv.fail(http.StatusInternalServerError)
	store := srv.store(t, 0)

	if _, _, err := store.Stat(context.Background(), "rc-1"); err == nil {
		t.Fatal("Stat against a failing bucket reported success")
	}

	if got := srv.requests(); got != 2 {
		t.Fatalf("a failing bucket was tried %d times, want 2 (RetryMaxAttempts)", got)
	}
}

func TestGetReportsFoundAlongsideTheBody(t *testing.T) {
	// Written after the GCS backend accepted every write and missed every
	// read: the client uploads over the JSON API but fetches bodies over the
	// XML API, and the fake only served the JSON routes. The round-trip test
	// discarded `found` and panicked on a nil body, which named the symptom
	// and not the cause.
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		if err := store.Put(ctx, "rc-found", strings.NewReader("x")); err != nil {
			t.Fatalf("Put: %v", err)
		}

		body, found, err := store.Get(ctx, "rc-found")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !found {
			t.Fatal("Get reported a miss for a key that was just written")
		}
		defer body.Close()
	})
}

func TestListEnumeratesEveryStoredObject(t *testing.T) {
	// Reclaim depends on this: storage is the only authority on what storage
	// holds, so anything reconciling a bucket against a database has to be
	// able to enumerate the bucket.
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		want := map[string]int64{"rc-10": 3, "rc-11": 5, "rc-12": 1}

		for key, size := range want {
			if err := store.Put(ctx, key, strings.NewReader(strings.Repeat("x", int(size)))); err != nil {
				t.Fatalf("Put %s: %v", key, err)
			}
		}

		got := map[string]int64{}
		if err := store.List(ctx, func(a durable.Attributes) error {
			got[a.Key] = a.Size
			return nil
		}); err != nil {
			t.Fatalf("List: %v", err)
		}

		for key, size := range want {
			if got[key] != size {
				t.Errorf("List reported %s as %d bytes, want %d", key, got[key], size)
			}
		}
		if len(got) != len(want) {
			t.Errorf("List returned %d objects, want %d: %v", len(got), len(want), got)
		}
	})
}

func TestListStopsWhenTheCallbackFails(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		for _, key := range []string{"rc-20", "rc-21", "rc-22"} {
			if err := store.Put(ctx, key, strings.NewReader("x")); err != nil {
				t.Fatalf("Put %s: %v", key, err)
			}
		}

		stop := errors.New("stop")
		seen := 0
		err := store.List(ctx, func(durable.Attributes) error {
			seen++
			return stop
		})

		if !errors.Is(err, stop) {
			t.Fatalf("List returned %v, want the callback's error", err)
		}
		if seen != 1 {
			t.Fatalf("List called the callback %d times after it failed, want 1", seen)
		}
	})
}

func TestListSkipsObjectsBelongingToAnotherConsumer(t *testing.T) {
	// One bucket is expected to serve several consumers, separated by prefix.
	// A reclaim pass that enumerated the whole bucket would see the other
	// consumer's objects as orphans and delete them.
	srv := newFakeGCS(t)
	mine := srv.storeWithPrefix(t, "resource-caches", 0)
	theirs := srv.storeWithPrefix(t, "agent-snapshots", 0)

	ctx := context.Background()
	if err := mine.Put(ctx, "rc-1", strings.NewReader("mine")); err != nil {
		t.Fatalf("Put mine: %v", err)
	}
	if err := theirs.Put(ctx, "snap1", strings.NewReader("theirs")); err != nil {
		t.Fatalf("Put theirs: %v", err)
	}

	var keys []string
	if err := mine.List(ctx, func(a durable.Attributes) error {
		keys = append(keys, a.Key)
		return nil
	}); err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(keys) != 1 || keys[0] != "rc-1" {
		t.Fatalf("List returned %v, want only [rc-1]", keys)
	}

	// The filter has to be server-side. Fetching the other consumer's objects
	// and discarding them client-side gives the same answer at a cost that
	// grows with somebody else's data.
	if got := srv.lastListPrefix(t); got != "resource-caches/" {
		t.Errorf("List asked the server to filter by %q, want %q — the whole bucket is being enumerated", got, "resource-caches/")
	}
}

func TestStatReportsAVersionWhereTheBackendHasOne(t *testing.T) {
	// v4's snapshot store will want to pin the exact write it read. GCS has
	// generations and S3 has versionIds; a file has neither, and saying so is
	// more useful than inventing one.
	srv := newFakeGCS(t)
	store := srv.store(t, 0)
	ctx := context.Background()

	if err := store.Put(ctx, "rc-30", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	attrs, found, err := store.Stat(ctx, "rc-30")
	if err != nil || !found {
		t.Fatalf("Stat = (%v, %v), want (true, nil)", found, err)
	}
	if attrs.Version == "" {
		t.Error("GCS Stat reported no Version; generation should populate it")
	}

	fs, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	if err := fs.Put(ctx, "rc-30", strings.NewReader("x")); err != nil {
		t.Fatalf("fs Put: %v", err)
	}
	fsAttrs, _, err := fs.Stat(ctx, "rc-30")
	if err != nil {
		t.Fatalf("fs Stat: %v", err)
	}
	if fsAttrs.Version != "" {
		t.Errorf("fs Stat invented a Version %q; a file has no generation", fsAttrs.Version)
	}
}

// listObjects answers ListObjectsV2 with the XML the SDK expects.
func (f *fakeS3) listObjects(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")

	f.mu.Lock()
	var contents strings.Builder
	count := 0
	for key, body := range f.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		count++
		contents.WriteString("<Contents><Key>" + key + "</Key><Size>" +
			strconv.Itoa(len(body)) + "</Size></Contents>")
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Name>artifacts</Name><Prefix>` + prefix + `</Prefix>` +
		`<KeyCount>` + strconv.Itoa(count) + `</KeyCount>` +
		`<MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated>` +
		contents.String() +
		`</ListBucketResult>`))
}

// A key may carry one retention-class prefix, so an object lifecycle rule can
// expire whole classes at different ages — a task cache in days, a review
// perhaps never — without the store knowing what any class means.
//
// The fs backend is the one that has to work for this: an object store has no
// directories, so a slash is just a character in the name, but fs must create
// the prefix directory and then find its way back out again in List.
func TestClassPrefixedKeys(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()

		const key = "resource-caches/rc-abc"
		if err := store.Put(ctx, key, strings.NewReader("cached bytes")); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}

		body, found, err := store.Get(ctx, key)
		if err != nil || !found {
			t.Fatalf("Get(%q) = found %v, err %v", key, found, err)
		}
		defer body.Close()

		got, err := io.ReadAll(body)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if string(got) != "cached bytes" {
			t.Errorf("round-tripped %q", got)
		}

		// Reclaim depends on this: an object the store holds but cannot
		// enumerate is one nothing can ever delete.
		var listed []string
		if err := store.List(ctx, func(a durable.Attributes) error {
			listed = append(listed, a.Key)
			return nil
		}); err != nil {
			t.Fatalf("List: %v", err)
		}
		if !slices.Contains(listed, key) {
			t.Errorf("List did not report the prefixed key; got %v", listed)
		}

		if err := store.Delete(ctx, key); err != nil {
			t.Fatalf("Delete(%q): %v", key, err)
		}
		if _, found, _ := store.Stat(ctx, key); found {
			t.Error("the object survived Delete")
		}
	})
}

// Two classes must not collide, and deleting one class must not touch another.
func TestClassesAreIndependent(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()

		if err := store.Put(ctx, "short-lived/rc-abc", strings.NewReader("a")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := store.Put(ctx, "permanent/rc-abc", strings.NewReader("b")); err != nil {
			t.Fatalf("Put: %v", err)
		}

		if err := store.Delete(ctx, "short-lived/rc-abc"); err != nil {
			t.Fatalf("Delete: %v", err)
		}

		if _, found, _ := store.Stat(ctx, "permanent/rc-abc"); !found {
			t.Error("deleting one class removed the same identity in another")
		}
	})
}

// The store's own prefix composes in FRONT of the retention class, so an object
// is named "<prefix>/<class>/<identity>".
//
// This is what an operator's lifecycle rule has to match, and getting it wrong
// is silent: a rule written against "resource-caches/" when a prefix is
// configured matches nothing, expires nothing, and reports no error. The bucket
// just grows. Pinning the composition here so the shape the chart documents
// cannot drift from the shape the code writes.
func TestStorePrefixComposesInFrontOfTheRetentionClass(t *testing.T) {
	root := t.TempDir()

	store, err := durable.NewFS(filepath.Join(root, "cluster-a"), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	ctx := context.Background()
	if err := store.Put(ctx, "resource-caches/rc-abc", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// The fs backend makes the composition visible as a path; GCS and S3 build
	// the same string as an object name.
	if _, err := os.Stat(filepath.Join(root, "cluster-a", "resource-caches", "rc-abc")); err != nil {
		t.Errorf("expected <prefix>/<class>/<identity>; stat failed: %v", err)
	}

	// List reports keys WITHOUT the store prefix, so what it yields can be fed
	// straight back to Get and Delete. A reclaim pass depends on that round-trip.
	var listed []string
	if err := store.List(ctx, func(a durable.Attributes) error {
		listed = append(listed, a.Key)
		return nil
	}); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0] != "resource-caches/rc-abc" {
		t.Fatalf("List reported %v, want [resource-caches/rc-abc]", listed)
	}
	if err := store.Delete(ctx, listed[0]); err != nil {
		t.Errorf("a key from List did not round-trip to Delete: %v", err)
	}
}
