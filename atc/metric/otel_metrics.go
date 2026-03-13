package metric

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	buildDurationHistogram      otelmetric.Float64Histogram
	httpResponseTimeHistogram   otelmetric.Float64Histogram
	k8sPodStartupHistogram      otelmetric.Float64Histogram
	containersCreatedCounter    otelmetric.Float64Counter
	volumesCreatedCounter       otelmetric.Float64Counter
	k8sPodFailuresCounter       otelmetric.Int64Counter
	resourceCheckDurationHist   otelmetric.Float64Histogram
	workerHeartbeatAgeGauge     otelmetric.Float64Gauge
)

// InitOTelMetrics creates OTel instruments for core Concourse metrics.
func InitOTelMetrics() {
	meter := otel.Meter("concourse")

	h, err := meter.Float64Histogram(
		"concourse.build.duration",
		otelmetric.WithDescription("Duration of build execution in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		buildDurationHistogram = h
	}

	h, err = meter.Float64Histogram(
		"concourse.http.response_time",
		otelmetric.WithDescription("HTTP response time in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		httpResponseTimeHistogram = h
	}

	h, err = meter.Float64Histogram(
		"concourse.k8s.pod_startup_duration",
		otelmetric.WithDescription("K8s pod startup duration in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		k8sPodStartupHistogram = h
	}

	c, err := meter.Float64Counter(
		"concourse.containers.created",
		otelmetric.WithDescription("Number of containers created"),
	)
	if err == nil {
		containersCreatedCounter = c
	}

	c, err = meter.Float64Counter(
		"concourse.volumes.created",
		otelmetric.WithDescription("Number of volumes created"),
	)
	if err == nil {
		volumesCreatedCounter = c
	}

	ic, err := meter.Int64Counter(
		"concourse.k8s.pod_failures",
		otelmetric.WithDescription("Number of K8s pod failures by reason"),
	)
	if err == nil {
		k8sPodFailuresCounter = ic
	}

	h, err = meter.Float64Histogram(
		"concourse.resource.check_duration",
		otelmetric.WithDescription("Duration of resource check execution in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		resourceCheckDurationHist = h
	}

	g, err := meter.Float64Gauge(
		"concourse.worker.heartbeat_age",
		otelmetric.WithDescription("Seconds since last successful worker heartbeat"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		workerHeartbeatAgeGauge = g
	}
}

// RecordBuildDuration records a build execution duration as an OTel histogram observation.
func RecordBuildDuration(ctx context.Context, duration time.Duration, team, pipeline, job, status string) {
	if buildDurationHistogram == nil {
		return
	}
	buildDurationHistogram.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("build.team", team),
			attribute.String("build.pipeline", pipeline),
			attribute.String("build.job", job),
			attribute.String("build.status", status),
		),
	)
}

// RecordHTTPResponseTime records an HTTP response time as an OTel histogram observation.
func RecordHTTPResponseTime(ctx context.Context, duration time.Duration, method, route string, statusCode int) {
	if httpResponseTimeHistogram == nil {
		return
	}
	httpResponseTimeHistogram.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("http.method", method),
			attribute.String("http.route", route),
			attribute.Int("http.status_code", statusCode),
		),
	)
}

// RecordK8sPodStartupDuration records a K8s pod startup duration as an OTel histogram observation.
func RecordK8sPodStartupDuration(ctx context.Context, duration time.Duration) {
	if k8sPodStartupHistogram == nil {
		return
	}
	k8sPodStartupHistogram.Record(ctx, duration.Seconds())
}

// RecordContainersCreated records the number of containers created as an OTel counter.
func RecordContainersCreated(ctx context.Context, count float64) {
	if containersCreatedCounter == nil {
		return
	}
	containersCreatedCounter.Add(ctx, count)
}

// RecordVolumesCreated records the number of volumes created as an OTel counter.
func RecordVolumesCreated(ctx context.Context, count float64) {
	if volumesCreatedCounter == nil {
		return
	}
	volumesCreatedCounter.Add(ctx, count)
}

// RecordK8sPodFailure records a K8s pod failure with the reason (OOMKilled, Evicted, Error).
func RecordK8sPodFailure(ctx context.Context, reason string) {
	if k8sPodFailuresCounter == nil {
		return
	}
	k8sPodFailuresCounter.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("reason", reason),
		),
	)
}

// RecordResourceCheckDuration records the duration of a resource check.
func RecordResourceCheckDuration(ctx context.Context, duration time.Duration, resourceType, pipeline string) {
	if resourceCheckDurationHist == nil {
		return
	}
	resourceCheckDurationHist.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("resource_type", resourceType),
			attribute.String("pipeline", pipeline),
		),
	)
}

// RecordWorkerHeartbeatAge records how long since the last successful worker heartbeat.
func RecordWorkerHeartbeatAge(ctx context.Context, age time.Duration, workerName string) {
	if workerHeartbeatAgeGauge == nil {
		return
	}
	workerHeartbeatAgeGauge.Record(ctx, age.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("worker", workerName),
		),
	)
}
