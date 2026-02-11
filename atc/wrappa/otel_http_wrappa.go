package wrappa

import (
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
		wrapped[name] = otelhttp.NewHandler(handler, name)
	}

	return wrapped
}
