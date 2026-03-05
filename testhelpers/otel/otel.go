// Package otel provides shared OpenTelemetry helpers for Concourse test suites.
//
// It initializes a TracerProvider that exports spans via OTLP (gRPC or HTTP),
// and provides a Ginkgo ReportAfterEach hook that emits a "test.run" span for
// each test case with attributes like test name, suite, duration, pass/fail,
// and pipeline name.
//
// Activation is opt-in via environment variables:
//   - OTEL_EXPORTER_OTLP_ENDPOINT — gRPC endpoint (e.g., "tempo.monitoring.svc:4317")
//   - OTLP_HTTP_ENDPOINT — HTTP endpoint (e.g., "http://tempo-otlp.home")
//
// If both are set, gRPC is preferred (it's used for in-cluster communication).
// When neither is set, all functions are no-ops.
package otel

import (
	"context"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

var (
	tracerProvider *sdktrace.TracerProvider
	tracer         trace.Tracer
	configured     bool
	suiteName      string
)

// InitTestTracing sets up an OTLP trace exporter for the test suite.
// It reads endpoints from OTEL_EXPORTER_OTLP_ENDPOINT (gRPC) or
// OTLP_HTTP_ENDPOINT (HTTP). If neither is set, tracing is disabled.
//
// Call from SynchronizedBeforeSuite or TestMain. Call Shutdown from
// the corresponding teardown.
func InitTestTracing(suite string) {
	suiteName = suite

	grpcEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	httpEndpoint := os.Getenv("OTLP_HTTP_ENDPOINT")

	if grpcEndpoint == "" && httpEndpoint == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var exporter sdktrace.SpanExporter
	var err error

	if grpcEndpoint != "" {
		exporter, err = otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(grpcEndpoint),
			otlptracegrpc.WithInsecure(),
		)
	} else {
		exporter, err = otlptracehttp.New(ctx,
			otlptracehttp.WithEndpoint(stripScheme(httpEndpoint)),
			otlptracehttp.WithInsecure(),
		)
	}

	if err != nil {
		ginkgo.GinkgoWriter.Printf("WARNING: failed to create OTLP exporter: %v\n", err)
		return
	}

	res := resource.NewSchemaless(
		semconv.ServiceNameKey.String("concourse-test"),
		attribute.String("test.suite", suite),
	)

	tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	tracer = tracerProvider.Tracer("concourse-test")
	configured = true
}

// Shutdown flushes and shuts down the tracer provider.
func Shutdown() {
	if tracerProvider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tracerProvider.Shutdown(ctx)
}

// IsConfigured returns whether tracing was successfully initialized.
func IsConfigured() bool {
	return configured
}

// Tracer returns the test suite's tracer. Returns a noop tracer if
// tracing is not configured.
func Tracer() trace.Tracer {
	if tracer == nil {
		return trace.NewNoopTracerProvider().Tracer("noop")
	}
	return tracer
}

// ReportTestSpan is a Ginkgo ReportAfterEach handler that creates a span
// for each completed test case. Register it in your suite:
//
//	var _ = ginkgo.ReportAfterEach(otel.ReportTestSpan)
func ReportTestSpan(report ginkgo.SpecReport) {
	if !configured {
		return
	}
	emitTestSpan(report, "")
}

// ReportTestSpanWithPipeline returns a ReportAfterEach handler that includes
// the pipeline name as a span attribute for correlation with server-side traces.
func ReportTestSpanWithPipeline(pipelineNameFn func() string) func(ginkgo.SpecReport) {
	return func(report ginkgo.SpecReport) {
		if !configured {
			return
		}
		emitTestSpan(report, pipelineNameFn())
	}
}

func emitTestSpan(report ginkgo.SpecReport, pipeline string) {
	attrs := []attribute.KeyValue{
		attribute.String("test.name", report.FullText()),
		attribute.String("test.suite", suiteName),
		attribute.String("test.state", report.State.String()),
		attribute.Float64("test.duration_s", report.RunTime.Seconds()),
		attribute.Int("test.attempt", report.NumAttempts),
	}

	if pipeline != "" {
		attrs = append(attrs, attribute.String("concourse.pipeline", pipeline))
	}

	if report.LeafNodeLocation.FileName != "" {
		attrs = append(attrs,
			attribute.String("test.file", report.LeafNodeLocation.FileName),
			attribute.Int("test.line", report.LeafNodeLocation.LineNumber),
		)
	}

	startTime := report.StartTime
	endTime := startTime.Add(report.RunTime)

	ctx := context.Background()
	_, span := tracer.Start(ctx, "test.run",
		trace.WithTimestamp(startTime),
		trace.WithAttributes(attrs...),
	)

	if report.State == types.SpecStateFailed || report.State == types.SpecStatePanicked {
		span.SetStatus(codes.Error, report.Failure.Message)
		if report.Failure.Message != "" {
			span.SetAttributes(attribute.String("test.failure", report.Failure.Message))
		}
	} else {
		span.SetStatus(codes.Ok, "")
	}

	span.End(trace.WithTimestamp(endTime))
}

func stripScheme(url string) string {
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(url, prefix) {
			return url[len(prefix):]
		}
	}
	return url
}
