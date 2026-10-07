package main

import (
	"context"
	"fmt"
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
// namespace different from the strict input and output ones, and the daemon
// refuses to start otherwise. This replaces the old guarantee that the cache
// could not compose an output object's key because of its depth.
func TestTheCacheNamespaceMustDifferFromInputAndOutput(t *testing.T) {
	for name, row := range map[string]struct {
		cache, input, output string
		ok                   bool
	}{
		"three different":         {"cache", "inputs", "outputs", true},
		"cache only":              {"cache", "", "", true},
		"no cache":                {"", "inputs", "outputs", true},
		"cache is the input":      {"shared", "shared", "", false},
		"cache is the output":     {"shared", "", "shared", false},
		"input is the output":     {"cache", "shared", "shared", false},
		"all three are one place": {"shared", "shared", "shared", false},
	} {
		err := validateStorageNamespaces(row.cache, row.input, row.output)
		if (err == nil) != row.ok {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Strict inputs switched off leave the input flag inert, so an operator
	// who left it pointing at the cache bucket is not refused for it.
	if err := validateStorageNamespaces("shared", hangarInputNamespace(false, "shared"), ""); err != nil {
		t.Errorf("an inert input namespace was compared: %v", err)
	}
	if err := validateStorageNamespaces("shared", hangarInputNamespace(true, "shared"), ""); err == nil {
		t.Error("an enabled input namespace equal to the cache was accepted")
	}

	// The output plane's namespace joins the comparison only when the daemon
	// mounts the plane with its output facet: a base-control-only plane, or no
	// plane, has no output bucket.
	capture := outputplane.Config{ControlKeyFile: "/control.key", OutputBucket: "shared"}
	if err := validateStorageNamespaces("shared", "", outputNamespace(capture)); err == nil {
		t.Error("an output plane publishing into the cache bucket was accepted")
	}
	if err := validateStorageNamespaces("cache", "shared", outputNamespace(capture)); err == nil {
		t.Error("an output plane publishing into the strict-input bucket was accepted")
	}
	unmounted := outputplane.Config{OutputBucket: "shared"}
	if got := outputNamespace(unmounted); got != "" {
		t.Errorf("a daemon with no output plane compared output namespace %q", got)
	}
	if got := outputNamespace(outputplane.Config{ControlKeyFile: "/control.key"}); got != "" {
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
