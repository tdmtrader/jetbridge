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

var _ = Describe("OTel Step Waiting", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelStepWaiting()
	})

	It("records steps waiting", func() {
		metric.RecordStepsWaiting(context.Background(), 4, "linux", "main", "task", "")

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.steps.waiting" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 4))

					attrSet := sum.DataPoints[0].Attributes
					platform, ok := attrSet.Value("platform")
					Expect(ok).To(BeTrue())
					Expect(platform.AsString()).To(Equal("linux"))

					teamName, ok := attrSet.Value("team.name")
					Expect(ok).To(BeTrue())
					Expect(teamName.AsString()).To(Equal("main"))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.steps.waiting metric")
	})

	It("records steps wait duration", func() {
		metric.RecordStepsWaitDuration(context.Background(), 2.5, "linux", "main", "get", "fast")

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.steps.wait_duration" {
					found = true
					hist, ok := m.Data.(metricdata.Histogram[float64])
					Expect(ok).To(BeTrue())
					Expect(hist.DataPoints).NotTo(BeEmpty())
					Expect(hist.DataPoints[0].Sum).To(BeNumerically(">=", 2.5))

					attrSet := hist.DataPoints[0].Attributes
					workerTags, ok := attrSet.Value("worker.tags")
					Expect(ok).To(BeTrue())
					Expect(workerTags.AsString()).To(Equal("fast"))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.steps.wait_duration metric")
	})
})
