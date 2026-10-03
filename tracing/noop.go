package tracing

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

var noopSpan trace.Span

func init() {
	tracer := trace.NewNoopTracerProvider().Tracer("")
	_, noopSpan = tracer.Start(context.Background(), "")
}
