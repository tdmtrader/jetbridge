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

var _ = Describe("OTel Scheduling", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelScheduling()
	})

	It("records jobs scheduled", func() {
		metric.RecordJobsScheduled(context.Background(), 10)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.jobs.scheduled" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 10))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.jobs.scheduled metric")
	})

	It("records jobs scheduling", func() {
		metric.RecordJobsScheduling(context.Background(), 3)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.jobs.scheduling" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 3))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.jobs.scheduling metric")
	})

	It("records scheduling job duration with attributes", func() {
		metric.RecordSchedulingJobDuration(context.Background(), 1.5, "my-pipeline", "my-job")

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.jobs.scheduling_duration" {
					found = true
					hist, ok := m.Data.(metricdata.Histogram[float64])
					Expect(ok).To(BeTrue())
					Expect(hist.DataPoints).NotTo(BeEmpty())
					Expect(hist.DataPoints[0].Sum).To(BeNumerically(">=", 1.5))

					attrSet := hist.DataPoints[0].Attributes
					pipeline, ok := attrSet.Value("pipeline")
					Expect(ok).To(BeTrue())
					Expect(pipeline.AsString()).To(Equal("my-pipeline"))

					job, ok := attrSet.Value("job")
					Expect(ok).To(BeTrue())
					Expect(job.AsString()).To(Equal("my-job"))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.jobs.scheduling_duration metric")
	})
})
