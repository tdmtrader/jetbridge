package tracing

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
	"go.opentelemetry.io/otel/trace"
)

// LoggerWithSpan enriches a lager.Logger with trace_id and span_id from the
// current OpenTelemetry span in ctx. If the context carries no valid span the
// logger is returned unchanged.
func LoggerWithSpan(ctx context.Context, logger lager.Logger) lager.Logger {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.HasTraceID() {
		return logger
	}
	return logger.WithData(lager.Data{
		"trace_id": sc.TraceID().String(),
		"span_id":  sc.SpanID().String(),
	})
}
