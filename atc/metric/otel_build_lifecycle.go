package metric

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	buildsStartedCounter            otelmetric.Float64Counter
	buildsRunningUpDownCounter      otelmetric.Float64UpDownCounter
	buildsFinishedCounter           otelmetric.Float64Counter
	checkBuildsStartedCounter       otelmetric.Float64Counter
	checkBuildsRunningUpDownCounter otelmetric.Float64UpDownCounter
)

// InitOTelBuildLifecycle creates OTel instruments for build lifecycle metrics.
func InitOTelBuildLifecycle() {
	meter := otel.Meter("concourse")

	c, err := meter.Float64Counter(
		"concourse.builds.started",
		otelmetric.WithDescription("Number of builds started"),
	)
	if err == nil {
		buildsStartedCounter = c
	}

	ud, err := meter.Float64UpDownCounter(
		"concourse.builds.running",
		otelmetric.WithDescription("Number of builds currently running"),
	)
	if err == nil {
		buildsRunningUpDownCounter = ud
	}

	c, err = meter.Float64Counter(
		"concourse.builds.finished",
		otelmetric.WithDescription("Number of builds finished"),
	)
	if err == nil {
		buildsFinishedCounter = c
	}

	c, err = meter.Float64Counter(
		"concourse.check_builds.started",
		otelmetric.WithDescription("Number of check builds started"),
	)
	if err == nil {
		checkBuildsStartedCounter = c
	}

	ud, err = meter.Float64UpDownCounter(
		"concourse.check_builds.running",
		otelmetric.WithDescription("Number of check builds currently running"),
	)
	if err == nil {
		checkBuildsRunningUpDownCounter = ud
	}
}

// RecordBuildsStarted records the number of builds started as an OTel counter.
func RecordBuildsStarted(ctx context.Context, count float64) {
	if buildsStartedCounter == nil {
		return
	}
	buildsStartedCounter.Add(ctx, count)
}

// RecordBuildsRunning records the number of builds currently running.
func RecordBuildsRunning(ctx context.Context, count float64) {
	if buildsRunningUpDownCounter == nil {
		return
	}
	buildsRunningUpDownCounter.Add(ctx, count)
}

// RecordBuildFinished records a single build finished with a status attribute.
func RecordBuildFinished(ctx context.Context, status string) {
	if buildsFinishedCounter == nil {
		return
	}
	buildsFinishedCounter.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("build.status", status),
		),
	)
}

// RecordCheckBuildsStarted records the number of check builds started as an OTel counter.
func RecordCheckBuildsStarted(ctx context.Context, count float64) {
	if checkBuildsStartedCounter == nil {
		return
	}
	checkBuildsStartedCounter.Add(ctx, count)
}

// RecordCheckBuildsRunning records the number of check builds currently running.
func RecordCheckBuildsRunning(ctx context.Context, count float64) {
	if checkBuildsRunningUpDownCounter == nil {
		return
	}
	checkBuildsRunningUpDownCounter.Add(ctx, count)
}
