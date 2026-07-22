package metric

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type WorkflowRunReconcilerPassResult string

const (
	WorkflowRunReconcilerPassSuccess  WorkflowRunReconcilerPassResult = "success"
	WorkflowRunReconcilerPassDegraded WorkflowRunReconcilerPassResult = "degraded"
	WorkflowRunReconcilerPassError    WorkflowRunReconcilerPassResult = "error"
)

func (result WorkflowRunReconcilerPassResult) valid() bool {
	return result == WorkflowRunReconcilerPassSuccess ||
		result == WorkflowRunReconcilerPassDegraded ||
		result == WorkflowRunReconcilerPassError
}

type WorkflowRunReconcilerRowResult string

const (
	WorkflowRunReconcilerRowAdvanced WorkflowRunReconcilerRowResult = "advanced"
	WorkflowRunReconcilerRowDeferred WorkflowRunReconcilerRowResult = "deferred"
	WorkflowRunReconcilerRowFailed   WorkflowRunReconcilerRowResult = "failed"
)

func (result WorkflowRunReconcilerRowResult) valid() bool {
	return result == WorkflowRunReconcilerRowAdvanced ||
		result == WorkflowRunReconcilerRowDeferred ||
		result == WorkflowRunReconcilerRowFailed
}

var (
	workflowRunReconcilerPassCounter      otelmetric.Int64Counter
	workflowRunReconcilerRowCounter       otelmetric.Int64Counter
	workflowRunReconcilerLastSuccessGauge otelmetric.Int64Gauge
)

func InitOTelWorkflowRunReconciler() {
	meter := otel.Meter("concourse")

	passCounter, err := meter.Int64Counter(
		"concourse.agent.workflow_run_reconciler.passes",
		otelmetric.WithDescription("Workflow-run reconciliation passes by bounded result"),
	)
	if err == nil {
		workflowRunReconcilerPassCounter = passCounter
	}

	rowCounter, err := meter.Int64Counter(
		"concourse.agent.workflow_run_reconciler.rows",
		otelmetric.WithDescription("Workflow-run reconciliation rows by bounded result"),
	)
	if err == nil {
		workflowRunReconcilerRowCounter = rowCounter
	}

	lastSuccessGauge, err := meter.Int64Gauge(
		"concourse.agent.workflow_run_reconciler.last_success",
		otelmetric.WithDescription("Unix timestamp of the last fully successful workflow-run reconciliation pass"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		workflowRunReconcilerLastSuccessGauge = lastSuccessGauge
	}
}

func RecordWorkflowRunReconcilerPass(
	ctx context.Context,
	result WorkflowRunReconcilerPassResult,
	at time.Time,
) {
	if !result.valid() {
		return
	}
	if workflowRunReconcilerPassCounter != nil {
		workflowRunReconcilerPassCounter.Add(ctx, 1, otelmetric.WithAttributes(
			attribute.String("result", string(result)),
		))
	}
	if result == WorkflowRunReconcilerPassSuccess && workflowRunReconcilerLastSuccessGauge != nil {
		workflowRunReconcilerLastSuccessGauge.Record(ctx, at.Unix())
	}
}

func RecordWorkflowRunReconcilerRow(ctx context.Context, result WorkflowRunReconcilerRowResult) {
	if !result.valid() || workflowRunReconcilerRowCounter == nil {
		return
	}
	workflowRunReconcilerRowCounter.Add(ctx, 1, otelmetric.WithAttributes(
		attribute.String("result", string(result)),
	))
}
