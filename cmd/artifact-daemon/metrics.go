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
	registry        *prometheus.Registry
	resolveRequests *prometheus.CounterVec
	refusals        *prometheus.CounterVec
	resolveDuration *prometheus.HistogramVec
	peerFetch       *prometheus.CounterVec
	durableOps      *prometheus.CounterVec

	durableReclaimed      prometheus.Counter
	durableReclaimedBytes prometheus.Counter

	durableObjects   prometheus.Gauge
	durableBytes     prometheus.Gauge
	durableOldestAge prometheus.Gauge
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
		refusals: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "refusals_total",
			Help:      "Requests refused, by route and reason. Every refusal is a build that failed or a caller that was turned away, and none of them were visible before this existed. Both labels are BOUNDED SETS -- never derive a label from a key, path or error string, which would give this metric one series per request.",
		}, []string{"route", "reason"}),
		peerFetch: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "peer_fetch_total",
			Help:      "Total cross-node peer artifact fetches, by status (ok/error).",
		}, []string{"status"}),
		durableOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_operations_total",
			Help:      "Durable-store operations, by op (has/restore/store/delete) and outcome (hit/miss/ok/error/raced). This is a per-node counter of operations, so sum() across nodes is correct here -- unlike a store-size gauge, where every node reports the same total and sum() multiplies it by node count.",
		}, []string{"op", "outcome"}),

		// AGGREGATION WARNING, repeated in every Help string below because it is
		// the one way these are read wrong: these describe the SHARED STORE, not
		// this node. Every daemon reports the same number, so sum() across nodes
		// multiplies the store by node count. Use max by (...). Getting this
		// backwards has already caused one silent OOM in this project.
		// Counters, not gauges: these are this node's own deletions, so summing
		// across nodes is correct and is what you want -- the store is shared,
		// but the work of reclaiming it is not.
		durableReclaimed: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_reclaimed_objects_total",
			Help:      "Objects deleted from the durable store by this daemon's retention sweep. Per-node work, so sum() across nodes is correct.",
		}),
		durableReclaimedBytes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_reclaimed_bytes_total",
			Help:      "Bytes deleted from the durable store by this daemon's retention sweep. Per-node work, so sum() across nodes is correct.",
		}),
		durableObjects: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_store_objects",
			Help:      "Objects in the shared durable store. SHARED VALUE -- every node reports the same total; aggregate with max by(), never sum().",
		}),
		durableBytes: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_store_bytes",
			Help:      "Bytes in the shared durable store. SHARED VALUE -- every node reports the same total; aggregate with max by(), never sum().",
		}),
		durableOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_store_oldest_object_age_seconds",
			Help:      "Age of the oldest object in the durable store. Reclaim is a bucket lifecycle rule nothing here performs, so this is the only signal that the rule is working: it should plateau near the configured expiry and grow without bound if the rule matches nothing. SHARED VALUE -- aggregate with max by(), never sum().",
		}),
	}
	reg.MustRegister(m.resolveRequests, m.refusals, m.resolveDuration, m.peerFetch, m.durableOps,
		m.durableObjects, m.durableBytes, m.durableOldestAge,
		m.durableReclaimed, m.durableReclaimedBytes)

	// Initialize peer-fetch series to 0 so the family is always scrapeable and
	// rate() works from the first fetch (CounterVecs emit no series until a
	// label set is observed).
	m.peerFetch.WithLabelValues("ok")
	m.peerFetch.WithLabelValues("error")

	// Same reason: an alert on durable errors must be able to fire from the
	// first failure rather than waiting for a series to appear.
	for _, op := range []string{"has", "restore", "store", "delete"} {
		m.durableOps.WithLabelValues(op, "error")
	}
	m.durableOps.WithLabelValues("restore", "hit")
	m.durableOps.WithLabelValues("restore", "miss")
	m.durableOps.WithLabelValues("store", "ok")
	m.durableOps.WithLabelValues("list", "error")
	m.durableOps.WithLabelValues("list", "ok")

	return m
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

// recordDurable records one durable-store operation. A nil receiver is a no-op
// so Servers constructed without metrics stay safe.
func (m *metrics) recordDurable(op, outcome string) {
	if m == nil {
		return
	}
	m.durableOps.WithLabelValues(op, outcome).Inc()
}

// recordResidency publishes what the durable store currently holds.
//
// Only ever called after a successful enumeration: a failed one leaves the
// previous values standing, because a zero is indistinguishable from an empty
// store and "the bucket went to zero" is the worst possible false alert.
func (m *metrics) recordResidency(objects, bytes int64, oldest time.Duration) {
	if m == nil {
		return
	}

	m.durableObjects.Set(float64(objects))
	m.durableBytes.Set(float64(bytes))
	m.durableOldestAge.Set(oldest.Seconds())
}

// recordReclaimed adds one sweep's deletions.
func (m *metrics) recordReclaimed(objects, bytes int64) {
	if m == nil {
		return
	}

	m.durableReclaimed.Add(float64(objects))
	m.durableReclaimedBytes.Add(float64(bytes))
}

func (m *metrics) recordRefusal(route, reason string) {
	if m == nil {
		return
	}
	if route == "" {
		route = "unknown"
	}
	if reason == "" {
		reason = "unknown"
	}
	m.refusals.WithLabelValues(route, reason).Inc()
}
