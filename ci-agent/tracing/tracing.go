package tracing

import (
	"context"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const defaultServiceName = "ci-agent"

// shutdownFunc is stored so Shutdown() can flush the provider.
var shutdownFunc func(context.Context) error

// Init initializes the OTel tracer provider. If OTEL_EXPORTER_OTLP_ENDPOINT
// is not set, the global provider remains a noop and no resources are allocated.
// Returns a shutdown function that must be called before process exit.
func Init(ctx context.Context) error {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return nil
	}

	serviceName := os.Getenv("OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = defaultServiceName
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return err
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.TelemetrySDKLanguageGo,
		semconv.TelemetrySDKNameKey.String("opentelemetry"),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	otel.SetTracerProvider(tp)
	shutdownFunc = tp.Shutdown
	return nil
}

// Shutdown flushes and shuts down the tracer provider. Safe to call even if
// Init was not called or tracing is not configured (noop in that case).
func Shutdown(ctx context.Context) error {
	if shutdownFunc != nil {
		return shutdownFunc(ctx)
	}
	return nil
}

// Tracer returns a named tracer from the global provider.
func Tracer() trace.Tracer {
	return otel.GetTracerProvider().Tracer("ci-agent")
}
