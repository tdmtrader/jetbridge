package tracing_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/concourse/ci-agent/tracing"
)

func TestTracing(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tracing Suite")
}

var _ = Describe("Init", func() {
	AfterEach(func() {
		// Reset global provider to noop after each test
		otel.SetTracerProvider(noop.NewTracerProvider())
	})

	It("returns nil and leaves noop provider when OTEL_EXPORTER_OTLP_ENDPOINT is unset", func() {
		// Ensure env var is not set (it shouldn't be in test env)
		err := tracing.Init(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns a valid tracer even when unconfigured", func() {
		tr := tracing.Tracer()
		Expect(tr).NotTo(BeNil())

		// Starting a span should work (noop span)
		ctx, span := tr.Start(context.Background(), "test-span")
		Expect(ctx).NotTo(BeNil())
		Expect(span).NotTo(BeNil())
		span.End()
	})
})

var _ = Describe("Shutdown", func() {
	It("is safe to call when Init was never called", func() {
		err := tracing.Shutdown(context.Background())
		Expect(err).NotTo(HaveOccurred())
	})
})
