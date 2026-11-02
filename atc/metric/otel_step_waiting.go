package metric

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	stepsWaitingUpDownCounter  otelmetric.Float64UpDownCounter
	stepsWaitDurationHistogram otelmetric.Float64Histogram
)

// InitOTelStepWaiting creates OTel instruments for step waiting metrics.
func InitOTelStepWaiting() {
	meter := otel.Meter("concourse")

	ud, err := meter.Float64UpDownCounter(
		"concourse.steps.waiting",
		otelmetric.WithDescription("Number of steps currently waiting"),
	)
	if err == nil {
		stepsWaitingUpDownCounter = ud
	}

	h, err := meter.Float64Histogram(
		"concourse.steps.wait_duration",
		otelmetric.WithDescription("Duration steps spend waiting in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		stepsWaitDurationHistogram = h
	}
}

// RecordStepsWaiting records the number of steps currently waiting.
func RecordStepsWaiting(ctx context.Context, count float64, teamName, stepType string) {
	if stepsWaitingUpDownCounter == nil {
		return
	}
	stepsWaitingUpDownCounter.Add(ctx, count,
		otelmetric.WithAttributes(
			attribute.String("team.name", teamName),
			attribute.String("step.type", stepType),
		),
	)
}

// RecordStepsWaitDuration records the duration a step spent waiting.
func RecordStepsWaitDuration(ctx context.Context, durationSeconds float64, teamName, stepType string) {
	if stepsWaitDurationHistogram == nil {
		return
	}
	stepsWaitDurationHistogram.Record(ctx, durationSeconds,
		otelmetric.WithAttributes(
			attribute.String("team.name", teamName),
			attribute.String("step.type", stepType),
		),
	)
}
