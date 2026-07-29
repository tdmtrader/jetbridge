package exec

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestOTelCheckpointCaptureMetricsMapOnlyClosedCoordinatorValues(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelAgentCheckpoint()

	recorder := NewOTelCheckpointCaptureMetrics()
	ctx := context.Background()
	recorder.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{
		Kind: CheckpointCaptureMetricDuration, Phase: "archive", Trigger: CheckpointCaptureTriggerElapsed, Duration: time.Second,
	})
	recorder.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{
		Kind: CheckpointCaptureMetricOutcome, Outcome: "committed", Trigger: CheckpointCaptureTriggerCompletion,
	})
	recorder.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{
		Kind: CheckpointCaptureMetricOutcome, Outcome: "lost_work", Trigger: CheckpointCaptureTriggerPreemption, Duration: 2 * time.Second,
	})
	recorder.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{
		Kind: CheckpointCaptureMetricRetainedBytes, Trigger: CheckpointCaptureTriggerElapsed, Bytes: 4096,
	})
	// The coordinator must never turn opaque strings into OTel label values.
	recorder.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{
		Kind: CheckpointCaptureMetricDuration, Phase: "pod-uid", Trigger: CheckpointCaptureTriggerElapsed, Duration: time.Second,
	})

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}
	if got := checkpointMetricPoints(t, collected, "concourse.agent.checkpoint.duration"); got != 1 {
		t.Fatalf("duration points = %d, want 1", got)
	}
	if got := checkpointMetricPoints(t, collected, "concourse.agent.checkpoint.captures"); got != 1 {
		t.Fatalf("capture points = %d, want 1", got)
	}
	if got := checkpointMetricPoints(t, collected, "concourse.agent.checkpoint.lost_work"); got != 1 {
		t.Fatalf("lost-work points = %d, want 1", got)
	}
	if got := checkpointMetricPoints(t, collected, "concourse.agent.checkpoint.retained_bytes"); got != 1 {
		t.Fatalf("retained-byte points = %d, want 1", got)
	}
}

func checkpointMetricPoints(t *testing.T, collected metricdata.ResourceMetrics, name string) int {
	t.Helper()
	for _, scope := range collected.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name != name {
				continue
			}
			switch data := measurement.Data.(type) {
			case metricdata.Histogram[float64]:
				return len(data.DataPoints)
			case metricdata.Histogram[int64]:
				return len(data.DataPoints)
			case metricdata.Sum[int64]:
				return len(data.DataPoints)
			default:
				t.Fatalf("metric %q has unexpected data %#v", name, measurement.Data)
			}
		}
	}
	return 0
}
