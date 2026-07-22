package metric_test

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/concourse/concourse/atc/metric"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestWorkflowRunReconcilerMetricsUseOnlyBoundedResults(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelWorkflowRunReconciler()

	ctx := context.Background()
	metric.RecordWorkflowRunReconcilerPass(ctx, metric.WorkflowRunReconcilerPassSuccess, time.Unix(1_784_753_000, 0))
	metric.RecordWorkflowRunReconcilerPass(ctx, metric.WorkflowRunReconcilerPassDegraded, time.Unix(1_784_753_100, 0))
	metric.RecordWorkflowRunReconcilerPass(ctx, metric.WorkflowRunReconcilerPassError, time.Unix(1_784_753_200, 0))
	metric.RecordWorkflowRunReconcilerRow(ctx, metric.WorkflowRunReconcilerRowAdvanced)
	metric.RecordWorkflowRunReconcilerRow(ctx, metric.WorkflowRunReconcilerRowDeferred)
	metric.RecordWorkflowRunReconcilerRow(ctx, metric.WorkflowRunReconcilerRowFailed)

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	passes := findWorkflowRunMetric(t, collected, "concourse.agent.workflow_run_reconciler.passes")
	passSum, ok := passes.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("passes data = %T, want int64 sum", passes.Data)
	}
	assertBoundedResultPoints(t, passSum.DataPoints, []string{"degraded", "error", "success"})

	rows := findWorkflowRunMetric(t, collected, "concourse.agent.workflow_run_reconciler.rows")
	rowSum, ok := rows.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("rows data = %T, want int64 sum", rows.Data)
	}
	assertBoundedResultPoints(t, rowSum.DataPoints, []string{"advanced", "deferred", "failed"})

	lastSuccess := findWorkflowRunMetric(t, collected, "concourse.agent.workflow_run_reconciler.last_success")
	gauge, ok := lastSuccess.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("last-success data = %T, want int64 gauge", lastSuccess.Data)
	}
	if len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != 1_784_753_000 {
		t.Fatalf("last-success points = %#v, want the successful pass timestamp only", gauge.DataPoints)
	}
	if gauge.DataPoints[0].Attributes.Len() != 0 {
		t.Fatalf("last-success attributes = %v, want none", gauge.DataPoints[0].Attributes)
	}
}

func TestWorkflowRunReconcilerMetricsRejectUnknownResults(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	metric.InitOTelWorkflowRunReconciler()

	ctx := context.Background()
	metric.RecordWorkflowRunReconcilerPass(ctx, metric.WorkflowRunReconcilerPassResult("run-123"), time.Now())
	metric.RecordWorkflowRunReconcilerRow(ctx, metric.WorkflowRunReconcilerRowResult("port-secret"))

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &collected); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	for _, scope := range collected.ScopeMetrics {
		for _, measurement := range scope.Metrics {
			if measurement.Name == "concourse.agent.workflow_run_reconciler.passes" ||
				measurement.Name == "concourse.agent.workflow_run_reconciler.rows" ||
				measurement.Name == "concourse.agent.workflow_run_reconciler.last_success" {
				t.Fatalf("invalid high-cardinality result produced metric %q", measurement.Name)
			}
		}
	}
}

func findWorkflowRunMetric(t *testing.T, collected metricdata.ResourceMetrics, name string) metricdata.Metrics {
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

func assertBoundedResultPoints(t *testing.T, points []metricdata.DataPoint[int64], want []string) {
	t.Helper()
	var got []string
	for _, point := range points {
		if point.Value != 1 {
			t.Fatalf("point value = %d, want 1", point.Value)
		}
		if point.Attributes.Len() != 1 {
			t.Fatalf("attributes = %v, want only result", point.Attributes)
		}
		value, found := point.Attributes.Value(attribute.Key("result"))
		if !found {
			t.Fatalf("attributes = %v, missing result", point.Attributes)
		}
		got = append(got, value.AsString())
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("results = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("results = %v, want %v", got, want)
		}
	}
}
