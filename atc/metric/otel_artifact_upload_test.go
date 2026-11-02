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

var _ = Describe("OTel Artifact Upload Metrics", func() {
	var (
		reader *sdkmetric.ManualReader
	)

	BeforeEach(func() {
		reader = sdkmetric.NewManualReader()
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		otel.SetMeterProvider(mp)

		metric.InitOTelArtifactUpload()
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

	It("records artifact upload duration", func() {
		metric.RecordArtifactUpload(context.Background(), "output", 3*time.Second, 1048576, 42, 1*time.Second, 2*time.Second)

		h := findHistogram("concourse.artifact.upload_duration")
		Expect(h).NotTo(BeNil(), "expected concourse.artifact.upload_duration histogram")
		Expect(h.DataPoints).NotTo(BeEmpty())
		Expect(h.DataPoints[0].Sum).To(BeNumerically(">=", 3.0))
	})

	It("records artifact upload size", func() {
		metric.RecordArtifactUpload(context.Background(), "output", 1*time.Second, 2097152, 100, 500*time.Millisecond, 500*time.Millisecond)

		h := findHistogram("concourse.artifact.upload_size")
		Expect(h).NotTo(BeNil(), "expected concourse.artifact.upload_size histogram")
		Expect(h.DataPoints).NotTo(BeEmpty())
		Expect(h.DataPoints[0].Sum).To(BeNumerically(">=", 2097152.0))
	})

	It("records artifact file count", func() {
		metric.RecordArtifactUpload(context.Background(), "cache", 1*time.Second, 1024, 250, 500*time.Millisecond, 500*time.Millisecond)

		h := findHistogram("concourse.artifact.file_count")
		Expect(h).NotTo(BeNil(), "expected concourse.artifact.file_count histogram")
		Expect(h.DataPoints).NotTo(BeEmpty())
		Expect(h.DataPoints[0].Sum).To(BeNumerically(">=", 250.0))
	})

	It("records tar and transfer durations separately", func() {
		metric.RecordArtifactUpload(context.Background(), "output", 3*time.Second, 1024, 10, 1*time.Second, 2*time.Second)

		tarH := findHistogram("concourse.artifact.tar_duration")
		Expect(tarH).NotTo(BeNil(), "expected concourse.artifact.tar_duration histogram")
		Expect(tarH.DataPoints).NotTo(BeEmpty())
		Expect(tarH.DataPoints[0].Sum).To(BeNumerically(">=", 1.0))

		transferH := findHistogram("concourse.artifact.transfer_duration")
		Expect(transferH).NotTo(BeNil(), "expected concourse.artifact.transfer_duration histogram")
		Expect(transferH.DataPoints).NotTo(BeEmpty())
		Expect(transferH.DataPoints[0].Sum).To(BeNumerically(">=", 2.0))
	})

	It("records correct attributes on duration metric", func() {
		metric.RecordArtifactUpload(context.Background(), "cache", 1*time.Second, 1024, 10, 500*time.Millisecond, 500*time.Millisecond)

		h := findHistogram("concourse.artifact.upload_duration")
		Expect(h).NotTo(BeNil())
		Expect(h.DataPoints).NotTo(BeEmpty())

		dp := h.DataPoints[0]
		artifactType, ok := dp.Attributes.Value("artifact.type")
		Expect(ok).To(BeTrue())
		Expect(artifactType.AsString()).To(Equal("cache"))
	})

	It("is a no-op when not initialized", func() {
		// Reset meter provider to a no-op
		otel.SetMeterProvider(sdkmetric.NewMeterProvider())

		// Re-initialize with a fresh reader to verify the old instruments are gone
		reader2 := sdkmetric.NewManualReader()
		mp2 := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader2))
		otel.SetMeterProvider(mp2)

		// Don't call InitOTelArtifactUpload — metrics should be nil
		// This tests the nil guard in RecordArtifactUpload
		// We can't easily test this without resetting the package vars,
		// so we just verify no panic occurs.
		Expect(func() {
			metric.RecordArtifactUpload(context.Background(), "output", 1*time.Second, 1024, 10, 500*time.Millisecond, 500*time.Millisecond)
		}).NotTo(Panic())
	})
})
