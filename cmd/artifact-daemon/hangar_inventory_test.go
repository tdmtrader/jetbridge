package main_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

const (
	residencyDigestA = hangar.Digest("sha256:" +
		"2222222222222222222222222222222222222222222222222222222222222222")
	residencyDigestB = hangar.Digest("sha256:" +
		"3333333333333333333333333333333333333333333333333333333333333333")
	residencyDigestC = hangar.Digest("sha256:" +
		"4444444444444444444444444444444444444444444444444444444444444444")
)

// residencyServer builds a daemon whose durable authority is the supplied fake,
// returning both the HTTP surface used to scrape /metrics and the Server whose
// refresh is driven directly. The refresh is exercised synchronously rather
// than through the background loop so these assertions never depend on timing.
func residencyServer(t *testing.T, durable *hangarfakes.FakeStore) (*httptest.Server, *daemon.Server) {
	t.Helper()
	server := daemon.NewServer(lagertest.NewTestLogger("hangar-inventory"), t.TempDir(), "node-a")
	if durable != nil {
		server.SetHangarStore(durable)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, server
}

func attributesFor(t *testing.T, kind hangar.Kind, digest hangar.Digest, generation, compressed int64) hangar.Attributes {
	t.Helper()
	ref, err := hangar.NewObjectRef(kind, digest, generation)
	if err != nil {
		t.Fatal(err)
	}
	return hangar.Attributes{
		Ref:               ref,
		CompressedBytes:   compressed,
		UncompressedBytes: compressed * 2,
		CreatedAt:         time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC),
	}
}

// metricValue extracts one exact sample from a Prometheus text exposition. It
// matches the full series line so a metric name that is a prefix of another
// cannot satisfy the lookup.
func metricValue(t *testing.T, body, series string) (float64, bool) {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, series+" ") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, series+" "))
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		return value, true
	}
	return 0, false
}

func mustMetricValue(t *testing.T, body, series string) float64 {
	t.Helper()
	value, ok := metricValue(t, body, series)
	if !ok {
		t.Fatalf("series %q absent from /metrics:\n%s", series, body)
	}
	return value
}

// TestHangarInventoryReportsResidencyPerKind is the core of this work: the
// store's aggregate residency — how much it currently holds, not how many bytes
// have moved through it — must be observable per kind.
func TestHangarInventoryReportsResidencyPerKind(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	durable.ListReturns([]hangar.Attributes{
		attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096),
		attributesFor(t, hangar.KindSnapshot, residencyDigestB, 12, 2048),
		attributesFor(t, hangar.KindCheckpoint, residencyDigestC, 13, 1024),
	}, nil)

	ts, server := residencyServer(t, durable)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("RefreshHangarInventory: %v", err)
	}

	body := getMetrics(t, ts.URL)
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_objects{kind="snapshots"}`); got != 2 {
		t.Errorf("snapshot object count = %v, want 2", got)
	}
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_bytes{kind="snapshots"}`); got != 6144 {
		t.Errorf("snapshot resident bytes = %v, want 6144", got)
	}
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_objects{kind="checkpoints"}`); got != 1 {
		t.Errorf("checkpoint object count = %v, want 1", got)
	}
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_bytes{kind="checkpoints"}`); got != 1024 {
		t.Errorf("checkpoint resident bytes = %v, want 1024", got)
	}
}

// TestHangarInventoryDropsToZeroWhenStoreDrains verifies the gauge tracks
// residency downward too. A kind that no longer holds anything must read zero
// rather than retaining its last non-zero value, or reclamation would be
// invisible in exactly the metric an operator watches to confirm it worked.
func TestHangarInventoryDropsToZeroWhenStoreDrains(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	durable.ListReturns([]hangar.Attributes{
		attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096),
	}, nil)

	ts, server := residencyServer(t, durable)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	durable.ListReturns(nil, nil)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("second refresh: %v", err)
	}

	body := getMetrics(t, ts.URL)
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_objects{kind="snapshots"}`); got != 0 {
		t.Errorf("drained object count = %v, want 0", got)
	}
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_bytes{kind="snapshots"}`); got != 0 {
		t.Errorf("drained resident bytes = %v, want 0", got)
	}
}

// TestHangarInventoryFailureKeepsLastKnownGoodAndIsVisible covers the failure
// mode a cached gauge introduces. A refresh that fails must not publish a
// partial or zeroed total — that would read as a drained store — and it must be
// visible as a failure, because a frozen gauge is otherwise indistinguishable
// from a store whose size is genuinely steady.
func TestHangarInventoryFailureKeepsLastKnownGoodAndIsVisible(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	durable.ListReturns([]hangar.Attributes{
		attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096),
		attributesFor(t, hangar.KindSnapshot, residencyDigestB, 12, 2048),
	}, nil)

	ts, server := residencyServer(t, durable)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	freshAt := mustMetricValue(t, getMetrics(t, ts.URL),
		"artifact_daemon_hangar_inventory_timestamp_seconds")
	if freshAt <= 0 {
		t.Fatalf("successful refresh did not stamp a freshness timestamp (%v)", freshAt)
	}

	// A visitor that has already accepted some objects then fails partway is
	// the dangerous shape: the accumulator holds a real but incomplete total.
	durable.SetListStub(func(ctx context.Context, kind hangar.Kind, visit func(hangar.Attributes) error) error {
		if kind != hangar.KindSnapshot {
			return nil
		}
		if err := visit(attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096)); err != nil {
			return err
		}
		return errors.New("bucket listing interrupted")
	})
	if err := server.RefreshHangarInventory(context.Background()); err == nil {
		t.Fatal("expected a failing refresh to report its error")
	}

	body := getMetrics(t, ts.URL)
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_objects{kind="snapshots"}`); got != 2 {
		t.Errorf("object count after failed refresh = %v, want last-known-good 2", got)
	}
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_bytes{kind="snapshots"}`); got != 6144 {
		t.Errorf("resident bytes after failed refresh = %v, want last-known-good 6144", got)
	}
	if got := mustMetricValue(t, body,
		`artifact_daemon_hangar_inventory_refreshes_total{status="error"}`); got != 1 {
		t.Errorf("error refresh count = %v, want 1", got)
	}
	if got := mustMetricValue(t, body,
		"artifact_daemon_hangar_inventory_timestamp_seconds"); got != freshAt {
		t.Errorf("failed refresh advanced the freshness timestamp: %v -> %v", freshAt, got)
	}
}

// TestHangarInventoryPublishesNoKindWhenAnotherKindFails pins the
// all-or-nothing publish. A pass in which snapshots enumerate cleanly but
// checkpoints fail must publish neither: the capacity alert compares a
// store-wide total, and a total missing one kind is smaller than the truth
// while being indistinguishable from a legitimate reading. Publishing each kind
// as it completes would satisfy every other test in this file and still be
// wrong here.
func TestHangarInventoryPublishesNoKindWhenAnotherKindFails(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	durable.ListReturns([]hangar.Attributes{
		attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096),
		attributesFor(t, hangar.KindCheckpoint, residencyDigestC, 13, 1024),
	}, nil)

	ts, server := residencyServer(t, durable)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Snapshots now enumerate cleanly at a larger size; checkpoints fail.
	durable.SetListStub(func(ctx context.Context, kind hangar.Kind, visit func(hangar.Attributes) error) error {
		if kind == hangar.KindCheckpoint {
			return errors.New("checkpoint listing interrupted")
		}
		if err := visit(attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096)); err != nil {
			return err
		}
		return visit(attributesFor(t, hangar.KindSnapshot, residencyDigestB, 12, 999999))
	})
	if err := server.RefreshHangarInventory(context.Background()); err == nil {
		t.Fatal("expected the failing pass to report its error")
	}

	body := getMetrics(t, ts.URL)
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_bytes{kind="snapshots"}`); got != 4096 {
		t.Errorf("snapshot bytes = %v, want last-known-good 4096; a kind was published from an incomplete pass", got)
	}
	if got := mustMetricValue(t, body, `artifact_daemon_hangar_bytes{kind="checkpoints"}`); got != 1024 {
		t.Errorf("checkpoint bytes = %v, want last-known-good 1024", got)
	}
}

// TestHangarInventoryRefreshCountsAreScrapeableFromZero keeps the refresh
// counter usable by an alert before any failure has ever occurred.
func TestHangarInventoryRefreshCountsAreScrapeableFromZero(t *testing.T) {
	ts, _ := residencyServer(t, &hangarfakes.FakeStore{})

	body := getMetrics(t, ts.URL)
	for _, status := range []string{"ok", "error"} {
		series := `artifact_daemon_hangar_inventory_refreshes_total{status="` + status + `"}`
		if _, ok := metricValue(t, body, series); !ok {
			t.Errorf("series %q absent before first refresh:\n%s", series, body)
		}
	}
}

// TestHangarInventoryKeepsLabelsBounded guards the cardinality invariant the
// existing snapshot metrics already hold: per-object identity must never reach
// a label, or one series per stored object would be created.
func TestHangarInventoryKeepsLabelsBounded(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	durable.ListReturns([]hangar.Attributes{
		attributesFor(t, hangar.KindSnapshot, residencyDigestA, 11, 4096),
	}, nil)

	ts, server := residencyServer(t, durable)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("RefreshHangarInventory: %v", err)
	}

	body := getMetrics(t, ts.URL)
	trimmed := strings.TrimPrefix(string(residencyDigestA), "sha256:")
	if strings.Contains(body, trimmed) {
		t.Fatalf("durable object digest leaked into metric labels:\n%s", body)
	}
}

// TestHangarInventoryWithoutDurableStoreIsANoOp keeps the refresh safe on a
// daemon deployed without Hangar, which is the default configuration.
func TestHangarInventoryWithoutDurableStoreIsANoOp(t *testing.T) {
	ts, server := residencyServer(t, nil)

	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("refresh without a durable store should be a no-op, got: %v", err)
	}

	body := getMetrics(t, ts.URL)
	if _, ok := metricValue(t, body, `artifact_daemon_hangar_objects{kind="snapshots"}`); ok {
		t.Error("residency gauge published without a durable store configured")
	}
	if got := mustMetricValue(t, body,
		"artifact_daemon_hangar_inventory_timestamp_seconds"); got != 0 {
		t.Errorf("freshness timestamp = %v, want 0 when no store is configured", got)
	}
}

// TestHangarInventoryListsEveryKind fails if a kind is ever added to Hangar and
// left out of the residency sweep, which would silently under-report the total
// the capacity alert compares against.
func TestHangarInventoryListsEveryKind(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	_, server := residencyServer(t, durable)
	if err := server.RefreshHangarInventory(context.Background()); err != nil {
		t.Fatalf("RefreshHangarInventory: %v", err)
	}

	listed := map[hangar.Kind]bool{}
	for _, call := range durable.ListCalls() {
		listed[call.Kind] = true
	}
	for _, kind := range []hangar.Kind{hangar.KindSnapshot, hangar.KindCheckpoint} {
		if !listed[kind] {
			t.Errorf("kind %q was never listed; its residency would be invisible", kind)
		}
	}
}
