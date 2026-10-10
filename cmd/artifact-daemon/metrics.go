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
	}
	reg.MustRegister(m.resolveRequests, m.refusals, m.resolveDuration, m.peerFetch)

	// Initialize peer-fetch series to 0 so the family is always scrapeable and
	// rate() works from the first fetch (CounterVecs emit no series until a
	// label set is observed).
	m.peerFetch.WithLabelValues("ok")
	m.peerFetch.WithLabelValues("error")

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
