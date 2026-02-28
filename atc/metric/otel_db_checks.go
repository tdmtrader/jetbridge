package metric

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	dbQueriesCounter      otelmetric.Float64Counter
	dbConnectionsUpDown   otelmetric.Float64UpDownCounter
	checksStartedCounter  otelmetric.Float64Counter
	checksFinishedCounter otelmetric.Float64Counter
	checksEnqueuedCounter otelmetric.Float64Counter
)

// InitOTelDBChecks creates OTel instruments for DB and check lifecycle metrics.
func InitOTelDBChecks() {
	meter := otel.Meter("concourse")

	c, err := meter.Float64Counter(
		"concourse.db.queries",
		otelmetric.WithDescription("Number of database queries"),
	)
	if err == nil {
		dbQueriesCounter = c
	}

	ud, err := meter.Float64UpDownCounter(
		"concourse.db.connections",
		otelmetric.WithDescription("Number of open database connections"),
	)
	if err == nil {
		dbConnectionsUpDown = ud
	}

	c, err = meter.Float64Counter(
		"concourse.checks.started",
		otelmetric.WithDescription("Number of checks started"),
	)
	if err == nil {
		checksStartedCounter = c
	}

	c, err = meter.Float64Counter(
		"concourse.checks.finished",
		otelmetric.WithDescription("Number of checks finished"),
	)
	if err == nil {
		checksFinishedCounter = c
	}

	c, err = meter.Float64Counter(
		"concourse.checks.enqueued",
		otelmetric.WithDescription("Number of checks enqueued"),
	)
	if err == nil {
		checksEnqueuedCounter = c
	}
}

// RecordDBQueries records the number of database queries as an OTel counter.
func RecordDBQueries(ctx context.Context, count float64) {
	if dbQueriesCounter == nil {
		return
	}
	dbQueriesCounter.Add(ctx, count)
}

// RecordDBConnections records the number of open database connections as an OTel up-down counter.
func RecordDBConnections(ctx context.Context, count float64, dbName string) {
	if dbConnectionsUpDown == nil {
		return
	}
	dbConnectionsUpDown.Add(ctx, count,
		otelmetric.WithAttributes(
			attribute.String("db.name", dbName),
		),
	)
}

// RecordChecksStarted records the number of checks started as an OTel counter.
func RecordChecksStarted(ctx context.Context, count float64) {
	if checksStartedCounter == nil {
		return
	}
	checksStartedCounter.Add(ctx, count)
}

// RecordChecksFinished records the number of checks finished as an OTel counter.
func RecordChecksFinished(ctx context.Context, count float64, status string) {
	if checksFinishedCounter == nil {
		return
	}
	checksFinishedCounter.Add(ctx, count,
		otelmetric.WithAttributes(
			attribute.String("status", status),
		),
	)
}

// RecordChecksEnqueued records the number of checks enqueued as an OTel counter.
func RecordChecksEnqueued(ctx context.Context, count float64) {
	if checksEnqueuedCounter == nil {
		return
	}
	checksEnqueuedCounter.Add(ctx, count)
}
