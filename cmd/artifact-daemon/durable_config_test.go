package main

import (
	"context"
	"fmt"
	"github.com/concourse/concourse/hangar"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"
	"github.com/concourse/concourse/hangar/gcstest"
	"github.com/concourse/concourse/hangar/objectstore"
)

// ADR-0002, as configuration: the fail-open cache is constructed against a
// namespace different from the output one, and the daemon refuses to start
// otherwise. This replaces the old guarantee that the cache could not compose
// an output object's key because of its depth.
func TestTheCacheNamespaceMustDifferFromOutput(t *testing.T) {
	for name, row := range map[string]struct {
		cache, output string
		ok            bool
	}{
		"two different":       {"cache", "outputs", true},
		"cache only":          {"cache", "", true},
		"output only":         {"", "outputs", true},
		"cache is the output": {"shared", "shared", false},
	} {
		err := validateStorageNamespaces(row.cache, row.output)
		if (err == nil) != row.ok {
			t.Errorf("%s: %v", name, err)
		}
	}

	// The output plane's namespace joins the comparison only when the daemon
	// mounts the plane (--execution-control) with its capture extension: a
	// base-control-only plane, or no plane, has no output bucket.
	capture := outputplane.Config{OutputBucket: "shared"}
	if err := validateStorageNamespaces("shared", outputNamespace(true, capture)); err == nil {
		t.Error("an output plane publishing into the cache bucket was accepted")
	}
	if got := outputNamespace(false, capture); got != "" {
		t.Errorf("a daemon with no output plane compared output namespace %q", got)
	}
	if got := outputNamespace(true, outputplane.Config{}); got != "" {
		t.Errorf("a base-control-only plane compared output namespace %q", got)
	}
}

func TestDurableTierConfiguration(t *testing.T) {
	logger := lagertest.NewTestLogger("durable-config")
	build := func(opts durableOptions) (*DurableTier, func() error, error) {
		return buildDurableTier(context.Background(), logger, newMetrics(), opts, nil)
	}

	tier, closeTier, err := build(durableOptions{})
	if err != nil || tier != nil || closeTier == nil {
		t.Fatalf("no store configured = (%v, %v), want a nil tier and no error", tier, err)
	}

	for _, kind := range []string{"s3", "filesystem"} {
		if _, _, err := build(durableOptions{kind: kind, bucket: "b"}); err == nil || !strings.Contains(err.Error(), "--durable-store") {
			t.Errorf("--durable-store=%s: %v, want a refusal naming the flag", kind, err)
		}
	}
	if _, _, err := build(durableOptions{kind: "gcs"}); err == nil {
		t.Error("gcs without a bucket was accepted")
	}
	if _, _, err := build(durableOptions{kind: "disk", bucket: "caches"}); err == nil {
		t.Error("disk without an endpoint, store id or token was accepted")
	}
	// An unreadable credential is configuration, not an outage.
	if _, _, err := build(durableOptions{kind: "disk", bucket: "caches", endpoint: "https://127.0.0.1:1",
		storeID: "store", tokenFile: "/nonexistent/token", timeout: time.Second}); err == nil {
		t.Error("an unreadable disk credential was treated as an unavailable store")
	}
}

// A store that ANSWERS and refuses -- a rejected credential, another store's
// identity -- is configuration, and the daemon exits on it. Only a store that
// cannot be reached is retried.
func TestARefusingCacheStoreIsAConfigurationError(t *testing.T) {
	for name, refusal := range map[string]error{
		"unauthorized":      fmt.Errorf("%w: 403", objectstore.ErrUnauthorized),
		"identity mismatch": fmt.Errorf("%w: disk identity mismatch", objectstore.ErrConflict),
	} {
		withOpen(t, func(context.Context, durable.Config) (durable.Store, func() error, error) {
			return nil, nil, refusal
		})
		_, _, err := buildDurableTier(context.Background(), lagertest.NewTestLogger("refused"), newMetrics(),
			durableOptions{kind: "gcs", bucket: "caches"}, nil)
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// An unreachable store leaves the tier unconnected -- every operation a miss,
// nothing advertised -- and connects in the background once the store answers.
func TestAnUnreachableCacheStoreConnectsInTheBackground(t *testing.T) {
	durableConnectInitialBackoff = 10 * time.Millisecond
	t.Cleanup(func() { durableConnectInitialBackoff = 5 * time.Second })

	memory := gcstest.NewMemory()
	memory.CreateBucket("caches")
	var attempts atomic.Int32
	withOpen(t, func(context.Context, durable.Config) (durable.Store, func() error, error) {
		if attempts.Add(1) < 3 {
			return nil, nil, fmt.Errorf("%w: connection refused", objectstore.ErrInfrastructure)
		}
		return durable.New(memory, memory, "caches", 0), func() error { return nil }, nil
	})

	connected := make(chan *DurableTier, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tier, _, err := buildDurableTier(ctx, lagertest.NewTestLogger("retry"), newMetrics(),
		durableOptions{kind: "gcs", bucket: "caches"}, func(tier *DurableTier) { connected <- tier })
	if err != nil || tier == nil {
		t.Fatalf("unreachable store = (%v, %v), want an unconnected tier", tier, err)
	}
	if tier.Ready() || tier.Has(ctx, "rc-1") {
		t.Fatal("an unconnected tier reported ready or a hit")
	}

	select {
	case got := <-connected:
		if got != tier || !tier.Ready() {
			t.Fatal("the tier did not connect")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the tier never connected after %d attempts", attempts.Load())
	}
}

func withOpen(t *testing.T, open func(context.Context, durable.Config) (durable.Store, func() error, error)) {
	t.Helper()
	previous := openDurableStore
	openDurableStore = open
	t.Cleanup(func() { openDurableStore = previous })
}

func TestTheOutputScratchIsDisjointFromTheStorageRoot(t *testing.T) {
	root := t.TempDir()
	at := func(rel string) string { return filepath.Join(root, rel) }
	for name, row := range map[string]struct {
		scratch, storage string
		ok               bool
	}{
		"disjoint":                          {at("output-scratch"), at("artifacts"), true},
		"unset":                             {"", at("artifacts"), true},
		"the storage root":                  {at("artifacts"), at("artifacts"), false},
		"inside the storage root":           {at("artifacts/scratch"), at("artifacts"), false},
		"holding the storage root":          {root, at("artifacts"), false},
		"a sibling sharing a string prefix": {at("artifacts-scratch"), at("artifacts"), true},
	} {
		err := validateOutputScratch(row.scratch, row.storage)
		if (err == nil) != row.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// kubelet creates an emptyDir 0777 without the sticky bit, which the
// canonicalizer refuses as a temp parent; the daemon owns the directory and
// tightens it before the plane uses it.
func TestTheOutputScratchIsMadePrivate(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "output-scratch")
	if err := os.Mkdir(scratch, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(scratch, 0777); err != nil {
		t.Fatal(err)
	}
	if err := validateOutputScratch(scratch, filepath.Join(root, "artifacts")); err != nil {
		t.Fatalf("a world-writable scratch was refused instead of tightened: %v", err)
	}
	info, err := os.Stat(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Fatalf("scratch mode = %o, want 0700", info.Mode().Perm())
	}
	if err := hangar.ValidateTempDir(scratch); err != nil {
		t.Fatalf("the tightened scratch does not validate as a temp parent: %v", err)
	}

	link := filepath.Join(root, "scratch-link")
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}
	if err := validateOutputScratch(link, filepath.Join(root, "artifacts")); err == nil {
		t.Fatal("a symlinked scratch directory was accepted")
	}
}
