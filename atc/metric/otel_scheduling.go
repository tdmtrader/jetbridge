package metric

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	jobsScheduledCounter          otelmetric.Float64Counter
	jobsSchedulingUpDownCounter   otelmetric.Float64UpDownCounter
	schedulingJobDurationHistogram otelmetric.Float64Histogram
)

// InitOTelScheduling creates OTel instruments for scheduling metrics.
func InitOTelScheduling() {
	meter := otel.Meter("concourse")

	c, err := meter.Float64Counter(
		"concourse.jobs.scheduled",
		otelmetric.WithDescription("Number of jobs scheduled"),
	)
	if err == nil {
		jobsScheduledCounter = c
	}

	ud, err := meter.Float64UpDownCounter(
		"concourse.jobs.scheduling",
		otelmetric.WithDescription("Number of jobs currently being scheduled"),
	)
	if err == nil {
		jobsSchedulingUpDownCounter = ud
	}

	h, err := meter.Float64Histogram(
		"concourse.jobs.scheduling_duration",
		otelmetric.WithDescription("Duration of job scheduling in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		schedulingJobDurationHistogram = h
	}
}

// RecordJobsScheduled records the number of jobs scheduled as an OTel counter.
func RecordJobsScheduled(ctx context.Context, count float64) {
	if jobsScheduledCounter == nil {
		return
	}
	jobsScheduledCounter.Add(ctx, count)
}

// RecordJobsScheduling records the number of jobs currently being scheduled.
func RecordJobsScheduling(ctx context.Context, count float64) {
	if jobsSchedulingUpDownCounter == nil {
		return
	}
	jobsSchedulingUpDownCounter.Add(ctx, count)
}

// RecordSchedulingJobDuration records the duration of scheduling a job.
func RecordSchedulingJobDuration(ctx context.Context, durationSeconds float64, pipeline, job string) {
	if schedulingJobDurationHistogram == nil {
		return
	}
	schedulingJobDurationHistogram.Record(ctx, durationSeconds,
		otelmetric.WithAttributes(
			attribute.String("pipeline", pipeline),
			attribute.String("job", job),
		),
	)
}
