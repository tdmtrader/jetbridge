package metric

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var stepDurationHistogram otelmetric.Float64Histogram

// InitOTelStepDuration creates the OTel histogram instrument for step duration.
func InitOTelStepDuration() {
	meter := otel.Meter("concourse")
	h, err := meter.Float64Histogram(
		"concourse.step.duration",
		otelmetric.WithDescription("Duration of pipeline step execution in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err != nil {
		return
	}
	stepDurationHistogram = h
}

// RecordStepDuration records a step execution duration as an OTel histogram observation.
func RecordStepDuration(ctx context.Context, stepType string, stepName string, duration time.Duration) {
	if stepDurationHistogram == nil {
		return
	}
	stepDurationHistogram.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("step.type", stepType),
			attribute.String("step.name", stepName),
		),
	)
}
