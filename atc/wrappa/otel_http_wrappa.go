package wrappa

import (
	"net/http"

	"github.com/tedsuo/rata"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type OTelHTTPWrappa struct{}

func NewOTelHTTPWrappa() Wrappa {
	return OTelHTTPWrappa{}
}

func (w OTelHTTPWrappa) Wrap(handlers rata.Handlers) rata.Handlers {
	wrapped := rata.Handlers{}

	for name, handler := range handlers {
		// The span is named after the route. otelhttp stopped doing that with
		// the operation it is given (v0.70 names server spans by method), so
		// the formatter says so explicitly.
		wrapped[name] = otelhttp.NewHandler(handler, name,
			otelhttp.WithSpanNameFormatter(func(operation string, _ *http.Request) string { return operation }))
	}

	return wrapped
}
