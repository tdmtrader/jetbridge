package wrappa_test

import (
	"net/http"
	"net/http/httptest"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/wrappa"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"github.com/tedsuo/rata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OTelHTTPWrappa", func() {
	var (
		spanRecorder    *tracetest.SpanRecorder
		inputHandlers   rata.Handlers
		wrappedHandlers rata.Handlers
	)

	BeforeEach(func() {
		spanRecorder = new(tracetest.SpanRecorder)
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
		tracing.ConfigureTraceProvider(provider)

		inputHandlers = rata.Handlers{}
		for _, route := range atc.Routes {
			inputHandlers[route.Name] = &stupidHandler{}
		}
	})

	AfterEach(func() {
		tracing.Configured = false
	})

	JustBeforeEach(func() {
		wrappedHandlers = wrappa.NewOTelHTTPWrappa().Wrap(inputHandlers)
	})

	It("wraps every handler", func() {
		Expect(wrappedHandlers).To(HaveLen(len(inputHandlers)))
		for name := range inputHandlers {
			Expect(wrappedHandlers).To(HaveKey(name))
		}
	})

	It("returns handlers that are different from the originals", func() {
		for name := range inputHandlers {
			// The wrapped handler should not be the same bare handler
			_, isStupid := wrappedHandlers[name].(*stupidHandler)
			Expect(isStupid).To(BeFalse(), "handler for route %s should be wrapped", name)
		}
	})

	Describe("span creation on request", func() {
		var (
			routeName string
			rw        *httptest.ResponseRecorder
			request   *http.Request
		)

		BeforeEach(func() {
			routeName = atc.GetInfo
			rw = httptest.NewRecorder()
			request = httptest.NewRequest("GET", "/api/v1/info", nil)
		})

		JustBeforeEach(func() {
			wrappedHandlers[routeName].ServeHTTP(rw, request)
		})

		It("creates a span for the request", func() {
			spans := spanRecorder.Started()
			Expect(spans).To(HaveLen(1))
		})

		It("names the span after the route", func() {
			spans := spanRecorder.Started()
			Expect(spans).To(HaveLen(1))
			Expect(spans[0].Name()).To(Equal(routeName))
		})

		It("records the span as an HTTP server span", func() {
			spans := spanRecorder.Started()
			Expect(spans).To(HaveLen(1))
			Expect(spans[0].SpanKind()).To(Equal(trace.SpanKindServer))
		})

		Context("with a different route", func() {
			BeforeEach(func() {
				routeName = atc.ListBuilds
				request = httptest.NewRequest("GET", "/api/v1/builds", nil)
			})

			It("names the span after that route", func() {
				spans := spanRecorder.Started()
				Expect(spans).To(HaveLen(1))
				Expect(spans[0].Name()).To(Equal(atc.ListBuilds))
			})
		})

		Context("with a POST request", func() {
			BeforeEach(func() {
				request = httptest.NewRequest("POST", "/api/v1/info", nil)
			})

			It("still creates a span", func() {
				spans := spanRecorder.Started()
				Expect(spans).To(HaveLen(1))
			})
		})
	})
})
