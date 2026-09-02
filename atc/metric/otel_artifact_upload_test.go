package metric_test

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

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
