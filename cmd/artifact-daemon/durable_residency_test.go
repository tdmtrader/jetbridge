package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// gaugeValue reads one gauge out of the registry by scraping it, so the test
// asserts what Prometheus would actually see rather than an internal field.
func gaugeValue(t *testing.T, m *metrics, name string) float64 {
	t.Helper()

	families, err := m.registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			return metric.GetGauge().GetValue()
		}
	}
	t.Fatalf("gauge %q not found in registry", name)

	return 0
}

func TestResidencyMeasuresWhatTheStoreHolds(t *testing.T) {
	store, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	m := newMetrics()
	ctx := context.Background()

	for _, body := range []string{"aaa", "bbbbb"} {
		if err := store.Put(ctx, "resource-caches/rc-"+body, strings.NewReader(body)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	NewResidencyReporter(lagertest.NewTestLogger("test"), store, m, time.Minute).measure(ctx)

	if got := gaugeValue(t, m, "artifact_daemon_durable_store_objects"); got != 2 {
		t.Errorf("objects = %v, want 2", got)
	}
	if got := gaugeValue(t, m, "artifact_daemon_durable_store_bytes"); got != 8 {
		t.Errorf("bytes = %v, want 8", got)
	}

	// The point of this gauge: reclaim is a bucket rule nothing here performs,
	// so age is the only signal that the rule is working. It should plateau near
	// the configured expiry, and grow without bound if the rule matches nothing.
	if got := gaugeValue(t, m, "artifact_daemon_durable_store_oldest_object_age_seconds"); got < 0 {
		t.Errorf("oldest age = %v, want a non-negative age", got)
	}
}

// A store failure must leave the last known values standing. Zeroing them is
// indistinguishable from "the bucket is empty", which is the single worst false
// alert this gauge could produce.
func TestResidencyKeepsLastValuesWhenTheStoreFails(t *testing.T) {
	store, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}
	m := newMetrics()
	ctx := context.Background()

	if err := store.Put(ctx, "rc-abc", strings.NewReader("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	reporter := NewResidencyReporter(lagertest.NewTestLogger("test"), store, m, time.Minute)
	reporter.measure(ctx)

	if got := gaugeValue(t, m, "artifact_daemon_durable_store_objects"); got != 1 {
		t.Fatalf("precondition: objects = %v, want 1", got)
	}

	// Now the store breaks.
	broken := NewResidencyReporter(lagertest.NewTestLogger("test"), brokenStore{}, m, time.Minute)
	broken.measure(ctx)

	if got := gaugeValue(t, m, "artifact_daemon_durable_store_objects"); got != 1 {
		t.Errorf("objects = %v after a failed enumeration; a zero reads as an empty bucket", got)
	}
	if got := gaugeValue(t, m, "artifact_daemon_durable_store_bytes"); got != 5 {
		t.Errorf("bytes = %v after a failed enumeration, want the previous value", got)
	}
}

// The reporter must stop when the daemon is shutting down rather than hold the
// process open mid-enumeration.
func TestResidencyRunStopsOnContextCancel(t *testing.T) {
	store, err := durable.NewFS(t.TempDir(), 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		NewResidencyReporter(lagertest.NewTestLogger("test"), store, newMetrics(), time.Hour).Run(ctx)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// A daemon with no durable tier has no store to walk.
func TestResidencyWithoutAStoreIsANoOp(t *testing.T) {
	done := make(chan struct{})

	go func() {
		defer close(done)
		NewResidencyReporter(lagertest.NewTestLogger("test"), nil, newMetrics(), time.Hour).Run(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked with no store configured")
	}
}
