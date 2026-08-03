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

			gauge, ok := m.Data.(metricdata.Gauge[float64])
			Expect(ok).To(BeTrue(), "connection depth must export as a gauge, not a sum")
			Expect(gauge.DataPoints).NotTo(BeEmpty())

			dp := gauge.DataPoints[0]
			dbName, ok := dp.Attributes.Value("db.name")
			Expect(ok).To(BeTrue())
			Expect(dbName.AsString()).To(Equal("api"))
		})

		// The caller samples the pool every tick and reports the absolute
		// depth, so an accumulating instrument exported the sum of every
		// sample ever taken: a pool sitting steady at 5 read 10, then 15,
		// climbing forever and tripping any threshold eventually.
		It("reports the latest depth rather than the sum of every sample", func() {
			metric.RecordDBConnections(context.Background(), 5, "api")
			metric.RecordDBConnections(context.Background(), 5, "api")
			metric.RecordDBConnections(context.Background(), 3, "api")

			var rm metricdata.ResourceMetrics
			Expect(reader.Collect(context.Background(), &rm)).To(Succeed())

			gauge, ok := findMetric(rm, "concourse.db.connections").Data.(metricdata.Gauge[float64])
			Expect(ok).To(BeTrue())
			Expect(gauge.DataPoints).To(HaveLen(1))
			Expect(gauge.DataPoints[0].Value).To(Equal(3.0))
		})

		It("keeps each database's depth separate", func() {
			metric.RecordDBConnections(context.Background(), 7, "api")
			metric.RecordDBConnections(context.Background(), 2, "backend")

			var rm metricdata.ResourceMetrics
			Expect(reader.Collect(context.Background(), &rm)).To(Succeed())

			gauge, ok := findMetric(rm, "concourse.db.connections").Data.(metricdata.Gauge[float64])
			Expect(ok).To(BeTrue())
			depths := map[string]float64{}
			for _, dp := range gauge.DataPoints {
				name, found := dp.Attributes.Value("db.name")
				Expect(found).To(BeTrue())
				depths[name.AsString()] = dp.Value
			}
			Expect(depths).To(Equal(map[string]float64{"api": 7, "backend": 2}))
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
