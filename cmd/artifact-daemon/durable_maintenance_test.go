package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
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

// newMaintainer wires a maintainer over a real store, with a real tier in
// between so deletes go through the same path production uses.
func newMaintainer(t *testing.T, store durable.Store, m *metrics, policy RetentionPolicy) *StoreMaintainer {
	t.Helper()

	logger := lagertest.NewTestLogger("test")
	tier := NewDurableTier(logger, store, m, time.Minute)

	return NewStoreMaintainer(logger, tier, m, time.Hour, policy)
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

	newMaintainer(t, store, m, nil).sweep(ctx)

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

	newMaintainer(t, store, m, nil).sweep(ctx)

	if got := gaugeValue(t, m, "artifact_daemon_durable_store_objects"); got != 1 {
		t.Fatalf("precondition: objects = %v, want 1", got)
	}

	// Now a real filesystem-backed store becomes unavailable.
	newMaintainer(t, unavailableFS(t), m, nil).sweep(ctx)

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
		newMaintainer(t, store, newMetrics(), nil).Run(ctx)
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
		newMaintainer(t, nil, newMetrics(), nil).Run(context.Background())
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run blocked with no store configured")
	}
}

// seed writes an object and back-dates it, so a test can express age directly
// rather than sleeping.
func seed(t *testing.T, root, key string, body string, age time.Duration) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

func present(t *testing.T, store durable.Store, key string) bool {
	t.Helper()

	_, found, err := store.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("Stat(%q): %v", key, err)
	}

	return found
}

// The whole feature, end to end: expired objects in a configured class go, and
// nothing else is touched.
func TestSweepReclaimsOnlyWhatThePolicyCovers(t *testing.T) {
	root := t.TempDir()
	store, err := durable.NewFS(root, 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	seed(t, root, "resource-caches/rc-old", "aaa", 48*time.Hour)
	seed(t, root, "resource-caches/rc-new", "bbb", time.Minute)
	seed(t, root, "reviews/rc-ancient", "ccc", 10000*time.Hour)
	seed(t, root, "rc-unclassed", "ddd", 10000*time.Hour)

	m := newMetrics()
	newMaintainer(t, store, m, RetentionPolicy{"resource-caches": 24 * time.Hour}).sweep(context.Background())

	if present(t, store, "resource-caches/rc-old") {
		t.Error("an expired object in a configured class survived")
	}
	if !present(t, store, "resource-caches/rc-new") {
		t.Error("an object younger than its retention period was deleted")
	}
	if !present(t, store, "reviews/rc-ancient") {
		t.Error("an object in an UNCONFIGURED class was deleted; silence must mean keep")
	}
	if !present(t, store, "rc-unclassed") {
		t.Error("an object with no class prefix was deleted")
	}

	// The gauges describe what the pass left behind, which is the state anyone
	// reading them cares about.
	if got := gaugeValue(t, m, "artifact_daemon_durable_store_objects"); got != 3 {
		t.Errorf("residency objects = %v, want 3 (the survivors)", got)
	}
	if got := gaugeValue(t, m, "artifact_daemon_durable_store_bytes"); got != 9 {
		t.Errorf("residency bytes = %v, want 9", got)
	}
}

// A daemon with no retention configured must never delete anything, whatever it
// finds. This is the state every deployment starts in.
func TestSweepWithNoPolicyDeletesNothing(t *testing.T) {
	root := t.TempDir()
	store, err := durable.NewFS(root, 0)
	if err != nil {
		t.Fatalf("NewFS: %v", err)
	}

	seed(t, root, "resource-caches/rc-ancient", "x", 10000*time.Hour)
	seed(t, root, "rc-also-ancient", "y", 10000*time.Hour)

	newMaintainer(t, store, newMetrics(), nil).sweep(context.Background())

	for _, key := range []string{"resource-caches/rc-ancient", "rc-also-ancient"} {
		if !present(t, store, key) {
			t.Errorf("%q was deleted with no retention policy configured", key)
		}
	}
}

// The failure mode that empties a bucket. A backend that reports no write time
// yields the zero value, which reads as 1970 -- so a naive age check would find
// every object ancient and delete the store on the first pass.
func TestSweepNeverDeletesAnObjectWithNoTimestamp(t *testing.T) {
	protocol := newS3ProtocolState(t)
	protocol.omitObjectTimes()
	store := protocol.store(t)
	ctx := context.Background()

	if err := store.Put(ctx, "resource-caches/rc-abc", strings.NewReader("x")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	newMaintainer(t, store, newMetrics(), RetentionPolicy{"resource-caches": time.Nanosecond}).sweep(ctx)

	if !present(t, store, "resource-caches/rc-abc") {
		t.Fatal("an object with no timestamp was deleted; a store that stops reporting times would be emptied")
	}
}

// One failed delete must not abort the pass -- the rest of the backlog still
// needs clearing, and the object it could not remove is simply removed later.
func TestSweepContinuesPastADeleteFailure(t *testing.T) {
	protocol := newS3ProtocolState(t)
	store := protocol.store(t)
	ctx := context.Background()
	for _, object := range []struct {
		key  string
		body string
	}{
		{key: "first-class/rc-a", body: "x"},
		{key: "second-class/rc-b", body: "y"},
		{key: "third-class/rc-c", body: "z"},
	} {
		if err := store.Put(ctx, object.key, strings.NewReader(object.body)); err != nil {
			t.Fatalf("seed %q: %v", object.key, err)
		}
		protocol.backdateObject(t, object.key, 48*time.Hour)
	}
	protocol.makeDeleteUnavailable("second-class/rc-b", http.StatusServiceUnavailable)

	newMaintainer(t, store, newMetrics(), RetentionPolicy{
		"first-class":  time.Hour,
		"second-class": time.Hour,
		"third-class":  time.Hour,
	}).sweep(ctx)

	for _, key := range []string{"first-class/rc-a", "third-class/rc-c"} {
		if present(t, store, key) {
			t.Errorf("%q survived; one delete failure aborted the whole pass", key)
		}
	}
	if !present(t, store, "second-class/rc-b") {
		t.Error("the object whose S3 deletion was unavailable should remain for the next pass")
	}
}
