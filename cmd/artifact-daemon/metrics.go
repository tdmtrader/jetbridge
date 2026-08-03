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
	checkpointOps      *prometheus.CounterVec
	checkpointBytes    *prometheus.CounterVec
	checkpointDuration *prometheus.HistogramVec
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
		checkpointOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "checkpoint_capture_operations_total",
			Help:      "Checkpoint archive and durable upload operations by bounded outcome.",
		}, []string{"operation", "status"}),
		checkpointBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "artifact_daemon",
			Name:      "checkpoint_capture_bytes_total",
			Help:      "Checkpoint archive and durable upload bytes by bounded outcome.",
		}, []string{"operation", "status"}),
		checkpointDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "artifact_daemon",
			Name:      "checkpoint_capture_duration_seconds",
			Help:      "Checkpoint archive and durable upload duration by bounded operation.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"operation", "status"}),
	}
	reg.MustRegister(
		m.resolveRequests,
		m.resolveDuration,
		m.peerFetch,
		m.snapshotOperations,
		m.snapshotBytes,
		m.snapshotDuration,
		m.checkpointOps,
		m.checkpointBytes,
		m.checkpointDuration,
	)

	// Initialize peer-fetch series to 0 so the family is always scrapeable and
	// rate() works from the first fetch (CounterVecs emit no series until a
	// label set is observed).
	m.peerFetch.WithLabelValues("ok")
	m.peerFetch.WithLabelValues("error")

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

func (m *metrics) recordCheckpoint(operation, status string, bytes int64, duration time.Duration) {
	if m == nil {
		return
	}
	if operation != "prepare" && operation != "upload" {
		operation = "unknown"
	}
	switch status {
	case "ok", "rejected", "expired", "unavailable", "failed":
	default:
		status = "failed"
	}
	m.checkpointOps.WithLabelValues(operation, status).Inc()
	if bytes > 0 {
		m.checkpointBytes.WithLabelValues(operation, status).Add(float64(bytes))
	}
	m.checkpointDuration.WithLabelValues(operation, status).Observe(duration.Seconds())
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
