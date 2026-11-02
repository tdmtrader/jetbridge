package metric_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OTel Step Duration Histogram", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelStepDuration()
	})

	It("records step duration", func() {
		metric.RecordStepDuration(context.Background(), "task", "my-task", 2*time.Second)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.step.duration" {
					found = true
					hist, ok := m.Data.(metricdata.Histogram[float64])
					Expect(ok).To(BeTrue())
					Expect(hist.DataPoints).NotTo(BeEmpty())
					Expect(hist.DataPoints[0].Sum).To(BeNumerically(">=", 2.0))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.step.duration metric")
	})

	It("records duration with correct attributes", func() {
		metric.RecordStepDuration(context.Background(), "get", "my-resource", 500*time.Millisecond)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.step.duration" {
					found = true
					hist := m.Data.(metricdata.Histogram[float64])
					Expect(hist.DataPoints).NotTo(BeEmpty())

					dp := hist.DataPoints[0]
					Expect(dp.Sum).To(BeNumerically(">=", 0.5))

					attrSet := dp.Attributes
					stepType, ok := attrSet.Value("step.type")
					Expect(ok).To(BeTrue())
					Expect(stepType.AsString()).To(Equal("get"))

					stepName, ok := attrSet.Value("step.name")
					Expect(ok).To(BeTrue())
					Expect(stepName.AsString()).To(Equal("my-resource"))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.step.duration metric")
	})
})
