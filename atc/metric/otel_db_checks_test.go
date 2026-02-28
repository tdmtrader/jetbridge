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

var _ = Describe("OTel DB and Checks Metrics", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelDBChecks()
	})

	findMetric := func(rm metricdata.ResourceMetrics, name string) *metricdata.Metrics {
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					return &m
				}
			}
		}
		return nil
	}

	Describe("RecordDBQueries", func() {
		It("records database query count", func() {
			metric.RecordDBQueries(context.Background(), 42)

			var rm metricdata.ResourceMetrics
			err := reader.Collect(context.Background(), &rm)
			Expect(err).NotTo(HaveOccurred())

			m := findMetric(rm, "concourse.db.queries")
			Expect(m).NotTo(BeNil(), "expected to find concourse.db.queries metric")

			sum, ok := m.Data.(metricdata.Sum[float64])
			Expect(ok).To(BeTrue())
			Expect(sum.DataPoints).NotTo(BeEmpty())
			Expect(sum.DataPoints[0].Value).To(BeNumerically(">=", 42.0))
		})
	})

	Describe("RecordDBConnections", func() {
		It("records database connections with db.name attribute", func() {
			metric.RecordDBConnections(context.Background(), 5, "api")

			var rm metricdata.ResourceMetrics
			err := reader.Collect(context.Background(), &rm)
			Expect(err).NotTo(HaveOccurred())

			m := findMetric(rm, "concourse.db.connections")
			Expect(m).NotTo(BeNil(), "expected to find concourse.db.connections metric")

			sum, ok := m.Data.(metricdata.Sum[float64])
			Expect(ok).To(BeTrue())
			Expect(sum.DataPoints).NotTo(BeEmpty())

			dp := sum.DataPoints[0]
			dbName, ok := dp.Attributes.Value("db.name")
			Expect(ok).To(BeTrue())
			Expect(dbName.AsString()).To(Equal("api"))
		})
	})

	Describe("RecordChecksStarted", func() {
		It("records checks started count", func() {
			metric.RecordChecksStarted(context.Background(), 10)

			var rm metricdata.ResourceMetrics
			err := reader.Collect(context.Background(), &rm)
			Expect(err).NotTo(HaveOccurred())

			m := findMetric(rm, "concourse.checks.started")
			Expect(m).NotTo(BeNil(), "expected to find concourse.checks.started metric")

			sum, ok := m.Data.(metricdata.Sum[float64])
			Expect(ok).To(BeTrue())
			Expect(sum.DataPoints).NotTo(BeEmpty())
			Expect(sum.DataPoints[0].Value).To(BeNumerically(">=", 10.0))
		})
	})

	Describe("RecordChecksFinished", func() {
		It("records checks finished with status attribute", func() {
			metric.RecordChecksFinished(context.Background(), 7, "error")

			var rm metricdata.ResourceMetrics
			err := reader.Collect(context.Background(), &rm)
			Expect(err).NotTo(HaveOccurred())

			m := findMetric(rm, "concourse.checks.finished")
			Expect(m).NotTo(BeNil(), "expected to find concourse.checks.finished metric")

			sum, ok := m.Data.(metricdata.Sum[float64])
			Expect(ok).To(BeTrue())
			Expect(sum.DataPoints).NotTo(BeEmpty())

			dp := sum.DataPoints[0]
			status, ok := dp.Attributes.Value("status")
			Expect(ok).To(BeTrue())
			Expect(status.AsString()).To(Equal("error"))
		})
	})

	Describe("RecordChecksEnqueued", func() {
		It("records checks enqueued count", func() {
			metric.RecordChecksEnqueued(context.Background(), 3)

			var rm metricdata.ResourceMetrics
			err := reader.Collect(context.Background(), &rm)
			Expect(err).NotTo(HaveOccurred())

			m := findMetric(rm, "concourse.checks.enqueued")
			Expect(m).NotTo(BeNil(), "expected to find concourse.checks.enqueued metric")

			sum, ok := m.Data.(metricdata.Sum[float64])
			Expect(ok).To(BeTrue())
			Expect(sum.DataPoints).NotTo(BeEmpty())
			Expect(sum.DataPoints[0].Value).To(BeNumerically(">=", 3.0))
		})
	})
})
