package main

import (
	"context"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
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
}

func TestDurableTierConfiguration(t *testing.T) {
	logger := lagertest.NewTestLogger("durable-config")

	tier, closeTier, err := buildDurableTier(context.Background(), logger, newMetrics(), durableOptions{})
	if err != nil || tier != nil || closeTier == nil {
		t.Fatalf("no store configured = (%v, %v), want a nil tier and no error", tier, err)
	}

	for _, kind := range []string{"s3", "filesystem"} {
		if _, _, err := buildDurableTier(context.Background(), logger, newMetrics(), durableOptions{kind: kind, bucket: "b"}); err == nil || !strings.Contains(err.Error(), "--durable-store") {
			t.Errorf("--durable-store=%s: %v, want a refusal naming the flag", kind, err)
		}
	}
	if _, _, err := buildDurableTier(context.Background(), logger, newMetrics(), durableOptions{kind: "gcs"}); err == nil {
		t.Error("gcs without a bucket was accepted")
	}
	if _, _, err := buildDurableTier(context.Background(), logger, newMetrics(), durableOptions{kind: "disk", bucket: "caches"}); err == nil {
		t.Error("disk without an endpoint, store id or token was accepted")
	}

	// Configured correctly but unreachable is not a startup failure: the tier
	// is fail-open, and an outage of the cache must not stop the daemon.
	tier, _, err = buildDurableTier(context.Background(), logger, newMetrics(), durableOptions{
		kind: "disk", bucket: "caches", endpoint: "https://127.0.0.1:1", storeID: "store",
		tokenFile: "/nonexistent/token", timeout: 1,
	})
	if err != nil || tier != nil {
		t.Fatalf("unreachable disk store = (%v, %v), want the tier off and no error", tier, err)
	}
}
