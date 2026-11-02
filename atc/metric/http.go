package metric

import (
	"net/http"
	"strconv"

	"github.com/felixge/httpsnoop"
	"go.opentelemetry.io/otel/trace"
)

// WrapHandler records the response time of every request handler serves,
// labelled with route, and attaches the request's trace as an exemplar.
func WrapHandler(route string, handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics := httpsnoop.CaptureMetrics(handler, w, r)

		var traceID string
		if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.HasTraceID() {
			traceID = sc.TraceID().String()
		}

		observeWithTrace(
			httpRequestsDuration.WithLabelValues(r.Method, route, strconv.Itoa(metrics.Code)),
			ms(metrics.Duration)/1000,
			traceID,
		)
		RecordHTTPResponseTime(r.Context(), metrics.Duration, r.Method, route, metrics.Code)
	})
}
