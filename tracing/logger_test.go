package tracing_test

import (
	"bytes"
	"context"
	"encoding/json"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("LoggerWithSpan", func() {
	It("adds trace_id and span_id when a span is active", func() {
		exporter := tracetest.NewInMemoryExporter()
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
		defer tp.Shutdown(context.Background())

		ctx, span := tp.Tracer("test").Start(context.Background(), "test-span")
		defer span.End()

		buf := new(bytes.Buffer)
		sink := lager.NewWriterSink(buf, lager.DEBUG)
		logger := lager.NewLogger("test")
		logger.RegisterSink(sink)

		enriched := tracing.LoggerWithSpan(ctx, logger)
		enriched.Info("hello")

		var entry map[string]interface{}
		err := json.Unmarshal(buf.Bytes(), &entry)
		Expect(err).NotTo(HaveOccurred())

		data, ok := entry["data"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(data).To(HaveKey("trace_id"))
		Expect(data).To(HaveKey("span_id"))

		sc := span.SpanContext()
		Expect(data["trace_id"]).To(Equal(sc.TraceID().String()))
		Expect(data["span_id"]).To(Equal(sc.SpanID().String()))
	})

	It("returns logger unchanged when no span is active", func() {
		buf := new(bytes.Buffer)
		sink := lager.NewWriterSink(buf, lager.DEBUG)
		logger := lager.NewLogger("test")
		logger.RegisterSink(sink)

		result := tracing.LoggerWithSpan(context.Background(), logger)
		result.Info("hello")

		var entry map[string]interface{}
		err := json.Unmarshal(buf.Bytes(), &entry)
		Expect(err).NotTo(HaveOccurred())

		data, ok := entry["data"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(data).NotTo(HaveKey("trace_id"))
		Expect(data).NotTo(HaveKey("span_id"))
	})
})
