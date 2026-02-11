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
