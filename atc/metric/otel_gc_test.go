package metric_test

import (
	"context"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OTel GC Collector Duration Histogram", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelGC()
	})

	It("records GC collector duration", func() {
		metric.RecordGCCollectorDuration(context.Background(), "build", 123.45)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.gc.collector_duration" {
					found = true
					hist, ok := m.Data.(metricdata.Histogram[float64])
					Expect(ok).To(BeTrue())
					Expect(hist.DataPoints).NotTo(BeEmpty())
					Expect(hist.DataPoints[0].Sum).To(BeNumerically(">=", 123.0))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.gc.collector_duration metric")
	})

	It("records duration with correct collector.name attribute", func() {
		metric.RecordGCCollectorDuration(context.Background(), "container", 50.0)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.gc.collector_duration" {
					found = true
					hist := m.Data.(metricdata.Histogram[float64])
					Expect(hist.DataPoints).NotTo(BeEmpty())

					dp := hist.DataPoints[0]
					collectorName, ok := dp.Attributes.Value("collector.name")
					Expect(ok).To(BeTrue())
					Expect(collectorName.AsString()).To(Equal("container"))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.gc.collector_duration metric")
	})
})
