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

func TestAgentCheckpointRecoveryMetricsUseOnlyClosedDimensions(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelAgentCheckpoint()

	ctx := context.Background()
	metric.RecordAgentInterruption(ctx, metric.AgentInterruptionPreempted)
	metric.RecordAgentRecovery(
		ctx,
		metric.AgentRecoveryWorkspaceOnly,
		metric.AgentRecoverySucceeded,
	)
	metric.RecordAgentRecovery(
		ctx,
		metric.AgentRecoveryCheckpointZero,
		metric.AgentRecoveryFailed,
	)
	metric.RecordAgentRecovery(
		ctx,
		metric.AgentRecoveryNotAdmitted,
		metric.AgentRecoveryManualReviewRequired,
	)
	metric.RecordAgentRestoreDuration(ctx, metric.AgentRestoreSucceeded, 2*time.Second)
	metric.RecordAgentAmbiguousEffects(ctx, 3)
	metric.RecordAgentCheckpointRetainedBytes(
		ctx,
		metric.AgentCheckpointTriggerElapsed,
		4096,
	)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}

	assertAgentCheckpointMetricAttributes(
		t,
		findAgentCheckpointMetric(t, collected, "concourse.agent.interruptions"),
		map[string][]string{"reason": {string(metric.AgentInterruptionPreempted)}},
	)
	assertAgentCheckpointMetricAttributes(
		t,
		findAgentCheckpointMetric(t, collected, "concourse.agent.recovery.attempts"),
		map[string][]string{
			"mode": {
				string(metric.AgentRecoveryWorkspaceOnly),
				string(metric.AgentRecoveryCheckpointZero),
				string(metric.AgentRecoveryNotAdmitted),
			},
			"outcome": {
				string(metric.AgentRecoverySucceeded),
				string(metric.AgentRecoveryFailed),
				string(metric.AgentRecoveryManualReviewRequired),
			},
		},
	)
	assertAgentCheckpointMetricAttributes(
		t,
		findAgentCheckpointMetric(t, collected, "concourse.agent.recovery.restore.duration"),
		map[string][]string{"outcome": {string(metric.AgentRestoreSucceeded)}},
	)

	ambiguous := findAgentCheckpointMetric(
		t,
		collected,
		"concourse.agent.recovery.ambiguous_effects",
	)
	ambiguousSum, ok := ambiguous.Data.(metricdata.Sum[int64])
	if !ok || len(ambiguousSum.DataPoints) != 1 ||
		ambiguousSum.DataPoints[0].Value != 3 ||
		ambiguousSum.DataPoints[0].Attributes.Len() != 0 {
		t.Fatalf("ambiguous-effect metric = %#v", ambiguous.Data)
	}

	retained := findAgentCheckpointMetric(
		t,
		collected,
		"concourse.agent.checkpoint.retained_bytes",
	)
	retainedHistogram, ok := retained.Data.(metricdata.Histogram[int64])
	if !ok || len(retainedHistogram.DataPoints) != 1 ||
		retainedHistogram.DataPoints[0].Sum != 4096 {
		t.Fatalf("retained-byte metric = %#v", retained.Data)
	}
	assertAgentCheckpointMetricAttributes(
		t,
		retained,
		map[string][]string{
			"trigger": {string(metric.AgentCheckpointTriggerElapsed)},
		},
	)
}

func TestAgentCheckpointRecoveryMetricsRejectUnknownDimensionsAndNegativeValues(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelAgentCheckpoint()

	ctx := context.Background()
	metric.RecordAgentInterruption(ctx, metric.AgentInterruptionReason("pod-uid"))
	metric.RecordAgentRecovery(
		ctx,
		metric.AgentRecoveryMode("build-42"),
		metric.AgentRecoverySucceeded,
	)
	metric.RecordAgentRecovery(
		ctx,
		metric.AgentRecoveryNativeResume,
		metric.AgentRecoveryOutcome("provider-error-text"),
	)
	metric.RecordAgentRestoreDuration(
		ctx,
		metric.AgentRestoreOutcome("node-name"),
		time.Second,
	)
	metric.RecordAgentRestoreDuration(ctx, metric.AgentRestoreFailed, -time.Second)
	metric.RecordAgentAmbiguousEffects(ctx, 0)
	metric.RecordAgentCheckpointRetainedBytes(
		ctx,
		metric.AgentCheckpointTrigger("run-id"),
		1,
	)
	metric.RecordAgentCheckpointRetainedBytes(
		ctx,
		metric.AgentCheckpointTriggerElapsed,
		-1,
	)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatal(err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			switch measurement.Name {
			case "concourse.agent.interruptions",
				"concourse.agent.recovery.attempts",
				"concourse.agent.recovery.restore.duration",
				"concourse.agent.recovery.ambiguous_effects",
				"concourse.agent.checkpoint.retained_bytes":
				t.Fatalf("invalid recovery dimension produced metric %q", measurement.Name)
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

func assertAgentCheckpointMetricAttributes(
	t *testing.T,
	measurement metricdata.Metrics,
	expected map[string][]string,
) {
	t.Helper()

	actual := map[string]map[string]struct{}{}
	add := func(attributes attribute.Set) {
		for key := range expected {
			value, found := attributes.Value(attribute.Key(key))
			if !found {
				t.Fatalf("metric %q attributes = %v, missing %q", measurement.Name, attributes, key)
			}
			if actual[key] == nil {
				actual[key] = map[string]struct{}{}
			}
			actual[key][value.AsString()] = struct{}{}
		}
	}

	switch data := measurement.Data.(type) {
	case metricdata.Sum[int64]:
		for _, point := range data.DataPoints {
			add(point.Attributes)
		}
	case metricdata.Histogram[float64]:
		for _, point := range data.DataPoints {
			add(point.Attributes)
		}
	case metricdata.Histogram[int64]:
		for _, point := range data.DataPoints {
			add(point.Attributes)
		}
	default:
		t.Fatalf("metric %q has unexpected data %#v", measurement.Name, measurement.Data)
	}

	for key, values := range expected {
		for _, value := range values {
			if _, found := actual[key][value]; !found {
				t.Fatalf("metric %q %s values = %v, missing %q", measurement.Name, key, actual[key], value)
			}
		}
	}
}
