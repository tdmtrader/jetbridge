package durable_test

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
	"github.com/concourse/concourse/hangar/disk"
	"github.com/concourse/concourse/hangar/diskserver"
	"github.com/concourse/concourse/hangar/gcstest"
)

const cacheBucket = "caches"

// Every backend is exercised through the same table: the in-memory object
// client, the real GCS adapter against fake-gcs-server, and the real disk
// store over its authenticated HTTP server as the cache role. The contract is
// what the daemon depends on, and it must hold over both real backends.
func eachStore(t *testing.T, run func(t *testing.T, store durable.Store)) {
	t.Helper()

	t.Run("memory", func(t *testing.T) { run(t, memoryStore(t, 0)) })
	t.Run("gcs", func(t *testing.T) { run(t, gcsStore(t, 0)) })
	t.Run("disk", func(t *testing.T) { run(t, diskStore(t, 0)) })
}

func memoryStore(t *testing.T, limit int64) durable.Store {
	t.Helper()
	memory := gcstest.NewMemory()
	memory.CreateBucket(cacheBucket)
	return durable.New(memory, memory, cacheBucket, limit)
}

func gcsStore(t *testing.T, limit int64) durable.Store {
	t.Helper()
	server, err := fakestorage.NewServerWithOptions(fakestorage.Options{Scheme: "http", Host: "127.0.0.1", Port: 0})
	if err != nil {
		t.Fatalf("starting fake-gcs-server: %v", err)
	}
	t.Cleanup(server.Stop)
	server.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: cacheBucket})

	store, closeStore, err := durable.Open(context.Background(), durable.Config{
		Kind: "gcs", Bucket: cacheBucket, Endpoint: server.URL(), Limit: limit,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = closeStore() })
	return store
}

func diskStore(t *testing.T, limit int64) durable.Store {
	t.Helper()
	root := t.TempDir()
	if err := disk.Initialize(root, "cache-store"); err != nil {
		t.Fatal(err)
	}
	objects, err := disk.Open(root, "cache-store", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	tokens := map[string]string{
		"input": strings.Repeat("i", 32), "publisher": strings.Repeat("p", 32),
		"inventory": strings.Repeat("v", 32), "reclaimer": strings.Repeat("r", 32),
		"cache": strings.Repeat("c", 32),
	}
	handler, err := diskserver.New(objects, diskserver.Config{
		StoreID: "cache-store", InputNamespace: "inputs", OutputNamespace: "outputs",
		CacheNamespace: cacheBucket, Credentials: tokens, MaxConcurrent: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte(tokens["cache"]), 0o600); err != nil {
		t.Fatal(err)
	}
	store, closeStore, err := durable.Open(context.Background(), durable.Config{
		Kind: "disk", Bucket: cacheBucket, Endpoint: server.URL, StoreID: "cache-store",
		TokenFile: token, CACert: ca, Timeout: 10 * time.Second, Limit: limit,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = closeStore() })
	return store
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
		if found || body != nil {
			t.Fatal("Get reported a hit for a key that was never written")
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
		if generation, err := strconv.ParseInt(attrs.Version, 10, 64); err != nil || generation <= 0 {
			t.Fatalf("Stat reported version %q, want the object's positive generation", attrs.Version)
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

// Objects are immutable. A second Put of a key is a second producer of the
// same content-derived cache, and finding the key present is success -- the
// create-if-absent precondition failing must never surface as an error, and
// must never replace the first object.
func TestPutOfAPresentKeyIsSuccessAndKeepsTheFirstObject(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()

		if err := store.Put(ctx, "rc-2", strings.NewReader("first")); err != nil {
			t.Fatalf("first Put: %v", err)
		}
		first, _, _ := store.Stat(ctx, "rc-2")
		if err := store.Put(ctx, "rc-2", strings.NewReader("first")); err != nil {
			t.Fatalf("second Put of the same key: %v", err)
		}
		second, _, _ := store.Stat(ctx, "rc-2")
		if first.Version != second.Version {
			t.Fatalf("the second Put replaced the object: generation %s became %s", first.Version, second.Version)
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

		// And a key expired and recreated is a new object, readable again.
		if err := store.Put(ctx, "rc-3", strings.NewReader("y")); err != nil {
			t.Fatalf("Put after Delete: %v", err)
		}
		if _, found, _ := store.Stat(ctx, "rc-3"); !found {
			t.Fatal("a key recreated after Delete is not there")
		}
	})
}

func TestACacheWithoutADeleteCapabilityCannotDelete(t *testing.T) {
	memory := gcstest.NewMemory()
	memory.CreateBucket(cacheBucket)
	store := durable.New(memory, nil, cacheBucket, 0)
	if err := store.Put(context.Background(), "rc-1", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "rc-1"); err == nil {
		t.Fatal("a cache built without a delete client deleted")
	}
}

func TestKeysThatAreNotACacheObjectNameAreRejected(t *testing.T) {
	// One slash is legal (a retention-class prefix). Everything here is not.
	invalid := []string{
		"../etc/passwd",
		"a/b/c", // two levels: a lifecycle rule matches a prefix, so depth is not free
		"a/../b",
		"a/..",
		"../b",
		"a/",
		"/b",
		"",
		".",
		"..",
		"/absolute",
		strings.Repeat("x", 256),
		"toolong/" + strings.Repeat("x", 256),
	}

	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		for _, key := range invalid {
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
	// consumes it fails somewhere far away from the cause. With immutable
	// objects it would also be permanent until it expired.
	for name, open := range map[string]func(*testing.T, int64) durable.Store{
		"memory": memoryStore, "gcs": gcsStore, "disk": diskStore,
	} {
		t.Run(name, func(t *testing.T) {
			store := open(t, 8)
			err := store.Put(context.Background(), "rc-big", strings.NewReader("more than eight bytes"))
			if !errors.Is(err, durable.ErrTooLarge) {
				t.Fatalf("Put over limit = %v, want ErrTooLarge", err)
			}
			if _, found, _ := store.Stat(context.Background(), "rc-big"); found {
				t.Fatal("an over-limit Put left an object behind")
			}
			if err := store.Put(context.Background(), "rc-exact", strings.NewReader("12345678")); err != nil {
				t.Fatalf("Put of exactly the limit: %v", err)
			}
		})
	}
}

func TestAFailedBodyLeavesNoObject(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		err := store.Put(context.Background(), "rc-partial", io.MultiReader(
			strings.NewReader("some bytes"),
			errReader{errors.New("connection reset")},
		))
		if err == nil {
			t.Fatal("Put with a failing body succeeded")
		}
		if _, found, _ := store.Stat(context.Background(), "rc-partial"); found {
			t.Fatal("a failed Put left the object visible")
		}
	})
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestListEnumeratesEveryStoredObjectWithItsWriteTime(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		want := []string{"rc-a", "resource-caches/rc-b", "reviews/rc-c"}
		for _, key := range want {
			if err := store.Put(ctx, key, strings.NewReader(key)); err != nil {
				t.Fatalf("Put(%q): %v", key, err)
			}
		}

		var got []string
		err := store.List(ctx, func(a durable.Attributes) error {
			got = append(got, a.Key)
			if a.Size != int64(len(a.Key)) {
				t.Errorf("%s: size %d, want %d", a.Key, a.Size, len(a.Key))
			}
			if a.Updated.IsZero() {
				t.Errorf("%s: no write time; retention cannot be applied to it", a.Key)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Fatalf("List = %v, want %v", got, want)
		}
	})
}

func TestListStopsWhenTheCallbackFails(t *testing.T) {
	eachStore(t, func(t *testing.T, store durable.Store) {
		ctx := context.Background()
		for _, key := range []string{"rc-a", "rc-b", "rc-c"} {
			if err := store.Put(ctx, key, strings.NewReader("x")); err != nil {
				t.Fatalf("Put: %v", err)
			}
		}
		stop := errors.New("stop")
		calls := 0
		err := store.List(ctx, func(durable.Attributes) error {
			calls++
			return stop
		})
		if !errors.Is(err, stop) || calls != 1 {
			t.Fatalf("List = (%v, %d calls), want the callback's error after one call", err, calls)
		}
	})
}

// A listing longer than one page must still be enumerated whole: retention
// that silently stopped at the first thousand objects reads exactly like a
// retention pass that finished.
func TestListPagesPastOnePage(t *testing.T) {
	memory := gcstest.NewMemory()
	memory.CreateBucket(cacheBucket)
	const total = 1203
	for i := range total {
		memory.Seed(cacheBucket, "rc-"+strconv.Itoa(i), []byte("x"), nil)
	}
	store := durable.New(memory, memory, cacheBucket, 0)

	seen := 0
	if err := store.List(context.Background(), func(durable.Attributes) error { seen++; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != total {
		t.Fatalf("List saw %d objects, want %d", seen, total)
	}
}

func TestConfigRefusesWhatCannotBeACache(t *testing.T) {
	for name, config := range map[string]durable.Config{
		"unknown store":   {Kind: "s3", Bucket: "b"},
		"filesystem":      {Kind: "filesystem", Bucket: "b"},
		"gcs, no bucket":  {Kind: "gcs"},
		"disk, no bucket": {Kind: "disk", Endpoint: "https://store", StoreID: "s", TokenFile: "/t"},
		"disk, no store":  {Kind: "disk", Bucket: "b", TokenFile: "/t", Endpoint: "https://store"},
		"disk, no token":  {Kind: "disk", Bucket: "b", StoreID: "s", Endpoint: "https://store"},
	} {
		if err := config.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
