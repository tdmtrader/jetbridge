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

var _ = Describe("OTel Core Metrics", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelMetrics()
	})

	findHistogram := func(name string) *metricdata.Histogram[float64] {
		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					h, ok := m.Data.(metricdata.Histogram[float64])
					if ok {
						return &h
					}
				}
			}
		}
		return nil
	}

	findSum := func(name string) *metricdata.Sum[float64] {
		var rm metricdata.ResourceMetrics
		err := reader.Collect(context.Background(), &rm)
		Expect(err).NotTo(HaveOccurred())
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				if m.Name == name {
					s, ok := m.Data.(metricdata.Sum[float64])
					if ok {
						return &s
					}
				}
			}
		}
		return nil
	}

	Describe("build duration histogram", func() {
		It("records build duration with attributes", func() {
			metric.RecordBuildDuration(context.Background(), 30*time.Second, "my-team", "my-pipeline", "my-job", "succeeded")

			h := findHistogram("concourse.build.duration")
			Expect(h).ToNot(BeNil(), "expected to find concourse.build.duration metric")
			Expect(h.DataPoints).NotTo(BeEmpty())
			Expect(h.DataPoints[0].Sum).To(BeNumerically(">=", 30.0))

			team, ok := h.DataPoints[0].Attributes.Value("build.team")
			Expect(ok).To(BeTrue())
			Expect(team.AsString()).To(Equal("my-team"))

			status, ok := h.DataPoints[0].Attributes.Value("build.status")
			Expect(ok).To(BeTrue())
			Expect(status.AsString()).To(Equal("succeeded"))
		})
	})

	Describe("HTTP response time histogram", func() {
		It("records HTTP response time with attributes", func() {
			metric.RecordHTTPResponseTime(context.Background(), 250*time.Millisecond, "GET", "/api/v1/info", 200)

			h := findHistogram("concourse.http.response_time")
			Expect(h).ToNot(BeNil(), "expected to find concourse.http.response_time metric")
			Expect(h.DataPoints).NotTo(BeEmpty())
			Expect(h.DataPoints[0].Sum).To(BeNumerically(">=", 0.25))

			method, ok := h.DataPoints[0].Attributes.Value("http.method")
			Expect(ok).To(BeTrue())
			Expect(method.AsString()).To(Equal("GET"))
		})
	})

	Describe("K8s pod startup duration histogram", func() {
		It("records pod startup duration", func() {
			metric.RecordK8sPodStartupDuration(context.Background(), 5*time.Second)

			h := findHistogram("concourse.k8s.pod_startup_duration")
			Expect(h).ToNot(BeNil(), "expected to find concourse.k8s.pod_startup_duration metric")
			Expect(h.DataPoints).NotTo(BeEmpty())
			Expect(h.DataPoints[0].Sum).To(BeNumerically(">=", 5.0))
		})
	})

	Describe("container and volume counters", func() {
		It("records containers created", func() {
			metric.RecordContainersCreated(context.Background(), 3)

			s := findSum("concourse.containers.created")
			Expect(s).ToNot(BeNil(), "expected to find concourse.containers.created metric")
			Expect(s.DataPoints).NotTo(BeEmpty())
			Expect(s.DataPoints[0].Value).To(BeNumerically(">=", 3.0))
		})

		It("records volumes created", func() {
			metric.RecordVolumesCreated(context.Background(), 5)

			s := findSum("concourse.volumes.created")
			Expect(s).ToNot(BeNil(), "expected to find concourse.volumes.created metric")
			Expect(s.DataPoints).NotTo(BeEmpty())
			Expect(s.DataPoints[0].Value).To(BeNumerically(">=", 5.0))
		})
	})
})
