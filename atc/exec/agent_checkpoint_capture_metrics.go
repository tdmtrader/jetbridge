package exec

import "time"

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
	RecordCheckpointCapture(CheckpointCaptureMetric)
}

type agentCheckpointCaptureOption func(*AgentCheckpointCapture)

func WithAgentCheckpointCaptureMetrics(metrics CheckpointCaptureMetrics) agentCheckpointCaptureOption {
	return func(coordinator *AgentCheckpointCapture) { coordinator.metrics = metrics }
}

func (coordinator *AgentCheckpointCapture) recordDuration(phase string, trigger CheckpointCaptureTrigger, duration time.Duration) {
	if coordinator.metrics != nil {
		coordinator.metrics.RecordCheckpointCapture(CheckpointCaptureMetric{Kind: CheckpointCaptureMetricDuration, Phase: phase, Trigger: trigger, Duration: duration})
	}
}

func (coordinator *AgentCheckpointCapture) recordOutcome(outcome string, trigger CheckpointCaptureTrigger, duration time.Duration) {
	if coordinator.metrics != nil {
		coordinator.metrics.RecordCheckpointCapture(CheckpointCaptureMetric{Kind: CheckpointCaptureMetricOutcome, Outcome: outcome, Trigger: trigger, Duration: duration})
	}
}
