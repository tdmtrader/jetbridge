package metric

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var gcCollectorDurationHistogram otelmetric.Float64Histogram

// InitOTelGC creates the OTel histogram instrument for GC collector duration.
func InitOTelGC() {
	meter := otel.Meter("concourse")
	h, err := meter.Float64Histogram(
		"concourse.gc.collector_duration",
		otelmetric.WithDescription("Duration of GC collector runs in milliseconds"),
		otelmetric.WithUnit("ms"),
	)
	if err != nil {
		return
	}
	gcCollectorDurationHistogram = h
}

// RecordGCCollectorDuration records a GC collector run duration as an OTel histogram observation.
func RecordGCCollectorDuration(ctx context.Context, collectorName string, durationMs float64) {
	if gcCollectorDurationHistogram == nil {
		return
	}
	gcCollectorDurationHistogram.Record(ctx, durationMs,
		otelmetric.WithAttributes(
			attribute.String("collector.name", collectorName),
		),
	)
}
