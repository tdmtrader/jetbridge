package metric

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

var (
	artifactUploadDurationHistogram  otelmetric.Float64Histogram
	artifactUploadSizeHistogram      otelmetric.Float64Histogram
	artifactFileCountHistogram       otelmetric.Float64Histogram
	artifactTarDurationHistogram     otelmetric.Float64Histogram
	artifactTransferDurationHistogram otelmetric.Float64Histogram
)

// InitOTelArtifactUpload creates OTel histogram instruments for artifact
// upload telemetry. This captures size, file count, and phase-separated
// timings (tar vs transfer) to inform architecture decisions.
func InitOTelArtifactUpload() {
	meter := otel.Meter("concourse")

	h, err := meter.Float64Histogram(
		"concourse.artifact.upload_duration",
		otelmetric.WithDescription("Total duration of artifact upload in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		artifactUploadDurationHistogram = h
	}

	h, err = meter.Float64Histogram(
		"concourse.artifact.upload_size",
		otelmetric.WithDescription("Size of uploaded artifact tar in bytes"),
		otelmetric.WithUnit("By"),
	)
	if err == nil {
		artifactUploadSizeHistogram = h
	}

	h, err = meter.Float64Histogram(
		"concourse.artifact.file_count",
		otelmetric.WithDescription("Number of files in uploaded artifact"),
	)
	if err == nil {
		artifactFileCountHistogram = h
	}

	h, err = meter.Float64Histogram(
		"concourse.artifact.tar_duration",
		otelmetric.WithDescription("Duration of tar creation phase in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		artifactTarDurationHistogram = h
	}

	h, err = meter.Float64Histogram(
		"concourse.artifact.transfer_duration",
		otelmetric.WithDescription("Duration of storage transfer phase in seconds"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		artifactTransferDurationHistogram = h
	}
}

// RecordArtifactUpload records artifact upload telemetry across all histogram
// instruments. Called after each artifact upload completes.
func RecordArtifactUpload(ctx context.Context, artifactType string, totalDuration time.Duration, sizeBytes int64, fileCount int64, tarDuration time.Duration, transferDuration time.Duration) {
	attrs := otelmetric.WithAttributes(
		attribute.String("artifact.type", artifactType),
	)

	if artifactUploadDurationHistogram != nil {
		artifactUploadDurationHistogram.Record(ctx, totalDuration.Seconds(), attrs)
	}
	if artifactUploadSizeHistogram != nil {
		artifactUploadSizeHistogram.Record(ctx, float64(sizeBytes), attrs)
	}
	if artifactFileCountHistogram != nil {
		artifactFileCountHistogram.Record(ctx, float64(fileCount), attrs)
	}
	if artifactTarDurationHistogram != nil {
		artifactTarDurationHistogram.Record(ctx, tarDuration.Seconds(), attrs)
	}
	if artifactTransferDurationHistogram != nil {
		artifactTransferDurationHistogram.Record(ctx, transferDuration.Seconds(), attrs)
	}
}
