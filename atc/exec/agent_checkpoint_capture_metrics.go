package exec

import (
	"context"
	"time"

	"github.com/concourse/concourse/atc/metric"
)

// CheckpointCaptureMetric is deliberately bounded: phase and outcome are
// closed coordinator constants and trigger is the closed server trigger set.
// It contains no build, plan, pod, digest, or provider-session label.
type CheckpointCaptureMetric struct {
	Kind     CheckpointCaptureMetricKind
	Phase    string
	Outcome  string
	Trigger  CheckpointCaptureTrigger
	Duration time.Duration
}

type CheckpointCaptureMetricKind string

const (
	CheckpointCaptureMetricDuration CheckpointCaptureMetricKind = "duration"
	CheckpointCaptureMetricOutcome  CheckpointCaptureMetricKind = "outcome"
)

type CheckpointCaptureMetrics interface {
	RecordCheckpointCapture(context.Context, CheckpointCaptureMetric)
}

// NewOTelCheckpointCaptureMetrics adapts only the coordinator's closed metric
// vocabulary to OTel. It intentionally accepts no caller-provided labels.
func NewOTelCheckpointCaptureMetrics() CheckpointCaptureMetrics {
	return otelCheckpointCaptureMetrics{}
}

type otelCheckpointCaptureMetrics struct{}

func (otelCheckpointCaptureMetrics) RecordCheckpointCapture(ctx context.Context, observed CheckpointCaptureMetric) {
	trigger, ok := otelCheckpointCaptureTrigger(observed.Trigger)
	if !ok {
		return
	}

	switch observed.Kind {
	case CheckpointCaptureMetricDuration:
		phase, ok := otelCheckpointCapturePhase(observed.Phase)
		if !ok {
			return
		}
		metric.RecordAgentCheckpointDuration(ctx, phase, trigger, observed.Duration)
	case CheckpointCaptureMetricOutcome:
		if observed.Outcome == "lost_work" {
			metric.RecordAgentCheckpointLostWork(ctx, trigger, observed.Duration)
			return
		}
		outcome, ok := otelCheckpointCaptureOutcome(observed.Outcome)
		if !ok {
			return
		}
		metric.RecordAgentCheckpointOutcome(ctx, outcome, trigger)
	}
}

func otelCheckpointCapturePhase(phase string) (metric.AgentCheckpointPhase, bool) {
	switch phase {
	case "requested_to_quiesced":
		return metric.AgentCheckpointRequestedToQuiesced, true
	case "archive":
		return metric.AgentCheckpointArchive, true
	case "upload":
		return metric.AgentCheckpointUpload, true
	case "total":
		return metric.AgentCheckpointTotal, true
	default:
		return "", false
	}
}

func otelCheckpointCaptureOutcome(outcome string) (metric.AgentCheckpointOutcome, bool) {
	switch outcome {
	case "committed":
		return metric.AgentCheckpointCommitted, true
	case "skipped":
		return metric.AgentCheckpointSkipped, true
	case "failed":
		return metric.AgentCheckpointFailed, true
	default:
		return "", false
	}
}

func otelCheckpointCaptureTrigger(trigger CheckpointCaptureTrigger) (metric.AgentCheckpointTrigger, bool) {
	switch trigger {
	case CheckpointCaptureTriggerElapsed:
		return metric.AgentCheckpointTriggerElapsed, true
	case CheckpointCaptureTriggerCompletion:
		return metric.AgentCheckpointTriggerCompletion, true
	case CheckpointCaptureTriggerExplicit:
		return metric.AgentCheckpointTriggerExplicit, true
	case CheckpointCaptureTriggerPreemption:
		return metric.AgentCheckpointTriggerPreemption, true
	default:
		return "", false
	}
}

type agentCheckpointCaptureOption func(*AgentCheckpointCapture)

func WithAgentCheckpointCaptureMetrics(metrics CheckpointCaptureMetrics) agentCheckpointCaptureOption {
	return func(coordinator *AgentCheckpointCapture) { coordinator.metrics = metrics }
}

func (coordinator *AgentCheckpointCapture) recordDuration(ctx context.Context, phase string, trigger CheckpointCaptureTrigger, duration time.Duration) {
	if coordinator.metrics != nil {
		coordinator.metrics.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{Kind: CheckpointCaptureMetricDuration, Phase: phase, Trigger: trigger, Duration: duration})
	}
}

func (coordinator *AgentCheckpointCapture) recordOutcome(ctx context.Context, outcome string, trigger CheckpointCaptureTrigger, duration time.Duration) {
	if coordinator.metrics != nil {
		coordinator.metrics.RecordCheckpointCapture(ctx, CheckpointCaptureMetric{Kind: CheckpointCaptureMetricOutcome, Outcome: outcome, Trigger: trigger, Duration: duration})
	}
}
