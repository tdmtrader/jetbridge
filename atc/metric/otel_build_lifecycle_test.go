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

var _ = Describe("OTel Build Lifecycle", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelBuildLifecycle()
	})

	It("records builds started", func() {
		metric.RecordBuildsStarted(context.Background(), 5)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.builds.started" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 5))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.builds.started metric")
	})

	It("records builds running", func() {
		metric.RecordBuildsRunning(context.Background(), 3)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.builds.running" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 3))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.builds.running metric")
	})

	It("records build finished with status attribute", func() {
		metric.RecordBuildFinished(context.Background(), "succeeded")

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.builds.finished" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())

					dp := sum.DataPoints[0]
					Expect(dp.Value).To(BeNumerically("==", 1))

					status, ok := dp.Attributes.Value("build.status")
					Expect(ok).To(BeTrue())
					Expect(status.AsString()).To(Equal("succeeded"))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.builds.finished metric")
	})

	It("records check builds started", func() {
		metric.RecordCheckBuildsStarted(context.Background(), 2)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.check_builds.started" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 2))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.check_builds.started metric")
	})

	It("records check builds running", func() {
		metric.RecordCheckBuildsRunning(context.Background(), 7)

		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		Expect(rm.ScopeMetrics).NotTo(BeEmpty())

		found := false
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == "concourse.check_builds.running" {
					found = true
					sum, ok := m.Data.(metricdata.Sum[float64])
					Expect(ok).To(BeTrue())
					Expect(sum.DataPoints).NotTo(BeEmpty())
					Expect(sum.DataPoints[0].Value).To(BeNumerically("==", 7))
				}
			}
		}
		Expect(found).To(BeTrue(), "expected to find concourse.check_builds.running metric")
	})
})
