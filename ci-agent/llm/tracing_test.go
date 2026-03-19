package llm_test

import (
	"context"
	"encoding/json"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/concourse/ci-agent/llm"
)

// stubClient is a test double that returns a preconfigured CallResult.
type stubClient struct {
	result llm.CallResult
	err    error
}

func (s *stubClient) Call(_ context.Context, _ string, _ llm.CallOpts) (llm.CallResult, error) {
	return s.result, s.err
}

func findAttr(attrs []attribute.KeyValue, key string) (attribute.KeyValue, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a, true
		}
	}
	return attribute.KeyValue{}, false
}

var _ = Describe("TracingClient", func() {
	var (
		exporter *tracetest.InMemoryExporter
		tp       *sdktrace.TracerProvider
	)

	BeforeEach(func() {
		exporter = tracetest.NewInMemoryExporter()
		tp = sdktrace.NewTracerProvider(
			sdktrace.WithSyncer(exporter),
		)
		otel.SetTracerProvider(tp)
	})

	AfterEach(func() {
		tp.Shutdown(context.Background())
		otel.SetTracerProvider(noop.NewTracerProvider())
	})

	It("creates a span with GenAI attributes on success", func() {
		inner := &stubClient{
			result: llm.CallResult{
				Result:        json.RawMessage(`{"ok": true}`),
				Model:         "claude-sonnet-4-6",
				Usage:         llm.Usage{InputTokens: 500, OutputTokens: 100, CacheReadInputTokens: 50},
				CostUSD:       0.015,
				DurationAPIMS: 2000,
				NumTurns:      1,
			},
		}

		tc := llm.NewTracingClient(inner)
		cr, err := tc.Call(context.Background(), "test prompt", llm.CallOpts{Model: "claude-sonnet-4-6"})
		Expect(err).NotTo(HaveOccurred())
		Expect(string(cr.Result)).To(ContainSubstring("ok"))

		spans := exporter.GetSpans()
		Expect(spans).To(HaveLen(1))

		span := spans[0]
		Expect(span.Name).To(Equal("gen_ai.invoke"))

		attrs := span.Attributes
		sys, ok := findAttr(attrs, llm.AttrGenAISystem)
		Expect(ok).To(BeTrue())
		Expect(sys.Value.AsString()).To(Equal("anthropic"))

		model, ok := findAttr(attrs, llm.AttrGenAIRequestModel)
		Expect(ok).To(BeTrue())
		Expect(model.Value.AsString()).To(Equal("claude-sonnet-4-6"))

		inputTok, ok := findAttr(attrs, llm.AttrGenAIUsageInputTokens)
		Expect(ok).To(BeTrue())
		Expect(inputTok.Value.AsInt64()).To(Equal(int64(500)))

		outputTok, ok := findAttr(attrs, llm.AttrGenAIUsageOutputTokens)
		Expect(ok).To(BeTrue())
		Expect(outputTok.Value.AsInt64()).To(Equal(int64(100)))

		cacheTok, ok := findAttr(attrs, llm.AttrGenAICacheReadTokens)
		Expect(ok).To(BeTrue())
		Expect(cacheTok.Value.AsInt64()).To(Equal(int64(50)))

		cost, ok := findAttr(attrs, llm.AttrGenAICostUSD)
		Expect(ok).To(BeTrue())
		Expect(cost.Value.AsFloat64()).To(BeNumerically("~", 0.015, 0.001))

		dur, ok := findAttr(attrs, llm.AttrGenAIDurationAPIMS)
		Expect(ok).To(BeTrue())
		Expect(dur.Value.AsInt64()).To(Equal(int64(2000)))

		turns, ok := findAttr(attrs, llm.AttrGenAINumTurns)
		Expect(ok).To(BeTrue())
		Expect(turns.Value.AsInt64()).To(Equal(int64(1)))
	})

	It("records error on the span when the inner client fails", func() {
		inner := &stubClient{
			err: errors.New("connection refused"),
		}

		tc := llm.NewTracingClient(inner)
		_, err := tc.Call(context.Background(), "test", llm.CallOpts{})
		Expect(err).To(HaveOccurred())

		spans := exporter.GetSpans()
		Expect(spans).To(HaveLen(1))

		span := spans[0]
		Expect(span.Status.Code.String()).To(Equal("Error"))
		Expect(span.Events).To(HaveLen(1)) // error event
	})

	It("omits optional attributes when values are zero", func() {
		inner := &stubClient{
			result: llm.CallResult{
				Result: json.RawMessage(`{}`),
				Model:  "claude-haiku-4-5",
				Usage:  llm.Usage{InputTokens: 10, OutputTokens: 5},
				// No cost, no cache tokens, no API duration, no turns
			},
		}

		tc := llm.NewTracingClient(inner)
		_, err := tc.Call(context.Background(), "test", llm.CallOpts{})
		Expect(err).NotTo(HaveOccurred())

		spans := exporter.GetSpans()
		Expect(spans).To(HaveLen(1))

		attrs := spans[0].Attributes
		_, ok := findAttr(attrs, llm.AttrGenAICostUSD)
		Expect(ok).To(BeFalse(), "cost should not be set when zero")
		_, ok = findAttr(attrs, llm.AttrGenAICacheReadTokens)
		Expect(ok).To(BeFalse(), "cache tokens should not be set when zero")
		_, ok = findAttr(attrs, llm.AttrGenAIDurationAPIMS)
		Expect(ok).To(BeFalse(), "API duration should not be set when zero")
		_, ok = findAttr(attrs, llm.AttrGenAINumTurns)
		Expect(ok).To(BeFalse(), "num turns should not be set when zero")
	})
})
