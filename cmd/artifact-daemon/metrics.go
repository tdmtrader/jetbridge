package main

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics holds the artifact-daemon Prometheus collectors. They live on a
// dedicated registry (not the global default) so each Server instance — and
// each test — is isolated.
type metrics struct {
	registry           *prometheus.Registry
	resolveRequests    *prometheus.CounterVec
	resolveDuration    *prometheus.HistogramVec
	peerFetch          *prometheus.CounterVec
	snapshotOperations *prometheus.CounterVec
	snapshotBytes      *prometheus.CounterVec
	snapshotDuration   *prometheus.HistogramVec

	// Residency, as distinct from throughput. Every collector above counts
	// bytes and operations that moved; none of them can answer "how much does
	// the store hold right now", because a counter that rises on PUT, GET and
	// DELETE alike describes traffic, not occupancy. These four express the
	// aggregate the store is actually bounded by.
	hangarObjects            *prometheus.GaugeVec
	hangarBytes              *prometheus.GaugeVec
	hangarInventoryRefreshes *prometheus.CounterVec
	hangarInventoryTimestamp prometheus.Gauge
}

// newMetrics builds and registers the daemon metric collectors.
func newMetrics() *metrics {
	reg := prometheus.NewRegistry()
	m := &metrics{
		registry: reg,
		resolveRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "resolve_requests_total",
			Help:      "Total artifact resolve requests, by resolution method (registry/filesystem/peer/exhausted) and status (ok/error/not_found).",
		}, []string{"method", "status"}),
		resolveDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "artifact_daemon",
			Name:      "resolve_duration_seconds",
			Help:      "Artifact resolve duration in seconds, by resolution method.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"method"}),
		peerFetch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "peer_fetch_total",
			Help:      "Total cross-node peer artifact fetches, by status (ok/error).",
		}, []string{"status"}),
		snapshotOperations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "snapshot_operations_total",
			Help:      "Total immutable snapshot operations by operation and bounded status.",
		}, []string{"operation", "status"}),
		snapshotBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "snapshot_bytes_total",
			Help:      "Total immutable snapshot bytes transferred by operation and bounded status.",
		}, []string{"operation", "status"}),
		snapshotDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "artifact_daemon",
			Name:      "snapshot_duration_seconds",
			Help:      "Immutable snapshot operation duration by operation and bounded status.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation", "status"}),
		hangarObjects: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "artifact_daemon",
			Name:      "hangar_objects",
			Help:      "Durable objects currently resident in the Hangar store, by kind. Store-wide, so every daemon reports the same total: aggregate with max(), never sum().",
		}, []string{"kind"}),
		hangarBytes: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "artifact_daemon",
			Name:      "hangar_bytes",
			Help:      "Compressed bytes currently resident in the Hangar store, by kind. Store-wide, so every daemon reports the same total: aggregate with max(), never sum().",
		}, []string{"kind"}),
		hangarInventoryRefreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "hangar_inventory_refreshes_total",
			Help:      "Hangar residency refresh passes by outcome (ok/error).",
		}, []string{"status"}),
		hangarInventoryTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "artifact_daemon",
			Name:      "hangar_inventory_timestamp_seconds",
			Help:      "Unix time of the last successful Hangar residency refresh. The residency gauges are cached, so without this a stalled refresh is indistinguishable from a store of steady size.",
		}),
	}
	reg.MustRegister(
		m.resolveRequests,
		m.resolveDuration,
		m.peerFetch,
		m.snapshotOperations,
		m.snapshotBytes,
		m.snapshotDuration,
		m.hangarObjects,
		m.hangarBytes,
		m.hangarInventoryRefreshes,
		m.hangarInventoryTimestamp,
	)

	// Initialize peer-fetch series to 0 so the family is always scrapeable and
	// rate() works from the first fetch (CounterVecs emit no series until a
	// label set is observed).
	m.peerFetch.WithLabelValues("ok")
	m.peerFetch.WithLabelValues("error")

	// Same reasoning for the refresh outcomes: the staleness alert has to be
	// able to evaluate before the first failure ever happens.
	m.hangarInventoryRefreshes.WithLabelValues("ok")
	m.hangarInventoryRefreshes.WithLabelValues("error")

	// The residency gauges are deliberately NOT initialized. A daemon deployed
	// without Hangar holds nothing, and publishing 0 there would be a claim
	// about a store it has no view of — max() across a mixed fleet would then
	// still be correct, but a fleet where every daemon lost its store would
	// read as a genuinely empty store rather than as absent data.

	return m
}

func (m *metrics) recordSnapshot(operation, status string, bytes int64, duration time.Duration) {
	if m == nil {
		return
	}
	switch operation {
	case "put", "get", "head", "delete", "repair-metadata":
	default:
		operation = "unknown"
	}
	switch status {
	case "ok", "created", "identical", "conflict", "digest_mismatch", "not_found",
		// Repair outcomes. "unrepairable" is the one that needs a human: the
		// object's bytes did not prove themselves, so nothing was rewritten.
		"unrepairable", "busy":
	default:
		status = "error"
	}
	m.snapshotOperations.WithLabelValues(operation, status).Inc()
	if bytes > 0 {
		m.snapshotBytes.WithLabelValues(operation, status).Add(float64(bytes))
	}
	m.snapshotDuration.WithLabelValues(operation, status).Observe(duration.Seconds())
}

// recordHangarResidency publishes one kind's aggregate occupancy. It is called
// only for a pass that enumerated the whole kind, so a zero here means the kind
// is genuinely empty rather than that listing stopped early.
func (m *metrics) recordHangarResidency(kind string, objects, bytes int64) {
	if m == nil {
		return
	}
	m.hangarObjects.WithLabelValues(kind).Set(float64(objects))
	m.hangarBytes.WithLabelValues(kind).Set(float64(bytes))
}

// recordHangarInventoryRefresh records the outcome of a residency pass. Only a
// successful pass advances the freshness timestamp: a failed pass leaves the
// previous totals in place, and the unchanged timestamp is what distinguishes
// those retained values from freshly observed ones.
func (m *metrics) recordHangarInventoryRefresh(status string, at time.Time) {
	if m == nil {
		return
	}
	if status != "ok" {
		status = "error"
	}
	m.hangarInventoryRefreshes.WithLabelValues(status).Inc()
	if status == "ok" {
		m.hangarInventoryTimestamp.Set(float64(at.Unix()))
	}
}

// recordResolve records the outcome of a single resolveOne call. A nil receiver
// is a no-op so Servers constructed without metrics stay safe.
func (m *metrics) recordResolve(method, status string, d time.Duration) {
	if m == nil {
		return
	}
	if method == "" {
		method = "unknown"
	}
	if status == "" {
		status = "unknown"
	}
	m.resolveRequests.WithLabelValues(method, status).Inc()
	m.resolveDuration.WithLabelValues(method).Observe(d.Seconds())
	if method == "peer" {
		m.peerFetch.WithLabelValues(status).Inc()
	}
}

// handler returns the Prometheus scrape handler for these metrics.
func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}
