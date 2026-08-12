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
	resolveDuration *prometheus.HistogramVec
	peerFetch       *prometheus.CounterVec
	durableOps      *prometheus.CounterVec
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
		durableOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "durable_operations_total",
			Help:      "Durable-store operations, by op (has/restore/store/delete) and outcome (hit/miss/ok/error/raced). This is a per-node counter of operations, so sum() across nodes is correct here -- unlike a store-size gauge, where every node reports the same total and sum() multiplies it by node count.",
		}, []string{"op", "outcome"}),
	}
	reg.MustRegister(m.resolveRequests, m.resolveDuration, m.peerFetch, m.durableOps)

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
