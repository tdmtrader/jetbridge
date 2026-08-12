package durable_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

func TestHasOfAbsentKeyIsFalseNotAnError(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		found, err := store.Has(context.Background(), "rc-404")
		if err != nil {
			t.Fatalf("Has of absent key returned an error: %v", err)
		}
		if found {
			t.Fatal("Has reported a hit for a key that was never written")
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

		found, err := store.Has(ctx, "rc-1")
		if err != nil || !found {
			t.Fatalf("Has after Put = (%v, %v), want (true, nil)", found, err)
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

		if found, _ := store.Has(ctx, "rc-3"); found {
			t.Fatal("key still present after Delete")
		}
	})
}

func TestKeysThatCouldEscapeTheStoreAreRejected(t *testing.T) {
	// The fs backend joins the key onto a root directory and S3 turns a slash
	// into a prefix, which would hide the object from Delete.
	escapes := []string{
		"../etc/passwd",
		"a/b",
		"",
		".",
		"..",
		"/absolute",
		strings.Repeat("x", 256),
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
			if _, err := store.Has(ctx, key); err == nil {
				t.Errorf("Has(%q) was accepted; it must be rejected", key)
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

		if found, _ := store.Has(context.Background(), "rc-big"); found {
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

	if found, _ := store.Has(context.Background(), "rc-partial"); found {
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

	if found, err := store.Has(ctx, "rc-9"); err != nil || !found {
		t.Fatalf("Has = (%v, %v), want (true, nil)", found, err)
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
	if _, err := store.Has(context.Background(), "rc-1"); err == nil {
		t.Fatal("Has against a failing bucket reported success")
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

	if _, err := store.Has(context.Background(), "rc-1"); err == nil {
		t.Fatal("Has against a failing bucket reported success")
	}

	if got := srv.requests(); got != 2 {
		t.Fatalf("a failing bucket was tried %d times, want 2 (RetryMaxAttempts)", got)
	}
}
