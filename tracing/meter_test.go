package tracing_test

import (
	"context"

	"github.com/concourse/concourse/tracing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var _ = Describe("Meter", func() {
	Describe("ConfigureMeterProvider", func() {
		It("sets the global OTel MeterProvider", func() {
			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

			tracing.ConfigureMeterProvider(mp)

			Expect(tracing.MetricsConfigured).To(BeTrue())

			// Verify the provider is usable: create a counter and record
			meter := otel.Meter("test")
			counter, err := meter.Int64Counter("test_counter")
			Expect(err).NotTo(HaveOccurred())

			ctx := context.Background()
			counter.Add(ctx, 1)
			var rm metricdata.ResourceMetrics
			err = reader.Collect(ctx, &rm)
			Expect(err).NotTo(HaveOccurred())
			Expect(rm.ScopeMetrics).NotTo(BeEmpty())
		})
	})

	Describe("MetricsConfig", func() {
		BeforeEach(func() {
			tracing.MetricsConfigured = false
		})

		It("configures metrics when OTLP address is provided", func() {
			c := tracing.MetricsConfig{
				OTLPAddress: "localhost:4317",
			}
			mp, shutdown, err := c.MeterProvider()
			Expect(err).NotTo(HaveOccurred())
			Expect(mp).NotTo(BeNil())
			Expect(shutdown).NotTo(BeNil())
		})

		It("configures metrics when GCP project ID is provided", func() {
			c := tracing.MetricsConfig{
				GCPProjectID: "my-project",
			}
			mp, shutdown, err := c.MeterProvider()
			Expect(err).NotTo(HaveOccurred())
			Expect(mp).NotTo(BeNil())
			Expect(shutdown).NotTo(BeNil())
		})

		It("returns nil when nothing is configured", func() {
			c := tracing.MetricsConfig{}
			mp, shutdown, err := c.MeterProvider()
			Expect(err).NotTo(HaveOccurred())
			Expect(mp).To(BeNil())
			Expect(shutdown).To(BeNil())
		})

		It("supports TLS for OTLP", func() {
			c := tracing.MetricsConfig{
				OTLPAddress: "localhost:4317",
				OTLPUseTLS:  true,
			}
			mp, shutdown, err := c.MeterProvider()
			Expect(err).NotTo(HaveOccurred())
			Expect(mp).NotTo(BeNil())
			Expect(shutdown).NotTo(BeNil())
		})

		It("supports custom headers for OTLP", func() {
			c := tracing.MetricsConfig{
				OTLPAddress: "localhost:4317",
				OTLPHeaders: map[string]string{"Authorization": "Bearer token"},
			}
			mp, shutdown, err := c.MeterProvider()
			Expect(err).NotTo(HaveOccurred())
			Expect(mp).NotTo(BeNil())
			Expect(shutdown).NotTo(BeNil())
		})
	})
})
