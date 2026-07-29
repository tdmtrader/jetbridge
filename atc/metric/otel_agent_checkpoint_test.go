package metric_test

import (
	"context"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestAgentCheckpointMetricsUseOnlyClosedPhasesOutcomesAndTriggers(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelAgentCheckpoint()

	ctx := context.Background()
	for _, phase := range []metric.AgentCheckpointPhase{
		metric.AgentCheckpointRequestedToQuiesced,
		metric.AgentCheckpointArchive,
		metric.AgentCheckpointUpload,
		metric.AgentCheckpointTotal,
	} {
		metric.RecordAgentCheckpointDuration(
			ctx,
			phase,
			metric.AgentCheckpointTriggerElapsed,
			time.Second,
		)
	}
	for _, outcome := range []metric.AgentCheckpointOutcome{
		metric.AgentCheckpointCommitted,
		metric.AgentCheckpointSkipped,
		metric.AgentCheckpointFailed,
	} {
		metric.RecordAgentCheckpointOutcome(
			ctx,
			outcome,
			metric.AgentCheckpointTriggerCompletion,
		)
	}
	metric.RecordAgentCheckpointLostWork(
		ctx,
		metric.AgentCheckpointTriggerPreemption,
		3*time.Second,
	)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}

	duration := findAgentCheckpointMetric(
		t,
		collected,
		"concourse.agent.checkpoint.duration",
	)
	histogram, ok := duration.Data.(metricdata.Histogram[float64])
	if !ok || len(histogram.DataPoints) != 4 {
		t.Fatalf("duration metric = %#v", duration.Data)
	}
	for _, point := range histogram.DataPoints {
		assertAgentCheckpointAttributes(
			t,
			point.Attributes,
			"phase",
			string(metric.AgentCheckpointTriggerElapsed),
		)
	}

	captures := findAgentCheckpointMetric(
		t,
		collected,
		"concourse.agent.checkpoint.captures",
	)
	sum, ok := captures.Data.(metricdata.Sum[int64])
	if !ok || len(sum.DataPoints) != 3 {
		t.Fatalf("capture metric = %#v", captures.Data)
	}
	for _, point := range sum.DataPoints {
		assertAgentCheckpointAttributes(
			t,
			point.Attributes,
			"outcome",
			string(metric.AgentCheckpointTriggerCompletion),
		)
	}

	lost := findAgentCheckpointMetric(
		t,
		collected,
		"concourse.agent.checkpoint.lost_work",
	)
	lostHistogram, ok := lost.Data.(metricdata.Histogram[float64])
	if !ok || len(lostHistogram.DataPoints) != 1 ||
		lostHistogram.DataPoints[0].Sum != 3 {
		t.Fatalf("lost-work metric = %#v", lost.Data)
	}
	assertAgentCheckpointAttributes(
		t,
		lostHistogram.DataPoints[0].Attributes,
		"",
		string(metric.AgentCheckpointTriggerPreemption),
	)
}

func TestAgentCheckpointMetricsRejectUnboundedLabelsAndNegativeDurations(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelAgentCheckpoint()

	ctx := context.Background()
	metric.RecordAgentCheckpointDuration(
		ctx,
		metric.AgentCheckpointPhase("build-42"),
		metric.AgentCheckpointTriggerElapsed,
		time.Second,
	)
	metric.RecordAgentCheckpointOutcome(
		ctx,
		metric.AgentCheckpointOutcome("pod-uid"),
		metric.AgentCheckpointTriggerCompletion,
	)
	metric.RecordAgentCheckpointLostWork(
		ctx,
		metric.AgentCheckpointTrigger("node-a"),
		time.Second,
	)
	metric.RecordAgentCheckpointDuration(
		ctx,
		metric.AgentCheckpointTotal,
		metric.AgentCheckpointTriggerElapsed,
		-time.Second,
	)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name == "concourse.agent.checkpoint.duration" ||
				measurement.Name == "concourse.agent.checkpoint.captures" ||
				measurement.Name == "concourse.agent.checkpoint.lost_work" {
				t.Fatalf("invalid label produced metric %q", measurement.Name)
			}
		}
	}
}

func findAgentCheckpointMetric(
	t *testing.T,
	collected metricdata.ResourceMetrics,
	name string,
) metricdata.Metrics {
	t.Helper()
	for _, scope := range collected.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name == name {
				return measurement
			}
		}
	}
	t.Fatalf("metric %q not found", name)
	return metricdata.Metrics{}
}

func assertAgentCheckpointAttributes(
	t *testing.T,
	attributes attribute.Set,
	dimension string,
	trigger string,
) {
	t.Helper()
	if attributes.Len() != 2 && dimension != "" ||
		attributes.Len() != 1 && dimension == "" {
		t.Fatalf("attributes = %v", attributes)
	}
	gotTrigger, found := attributes.Value(attribute.Key("trigger"))
	if !found || gotTrigger.AsString() != trigger {
		t.Fatalf("attributes = %v, trigger = %q", attributes, trigger)
	}
	if dimension != "" {
		if _, found := attributes.Value(attribute.Key(dimension)); !found {
			t.Fatalf("attributes = %v, missing %s", attributes, dimension)
		}
	}
}
