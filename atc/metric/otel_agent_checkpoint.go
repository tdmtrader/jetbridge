package metric

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

type AgentCheckpointPhase string

const (
	AgentCheckpointRequestedToQuiesced AgentCheckpointPhase = "requested_to_quiesced"
	AgentCheckpointArchive             AgentCheckpointPhase = "archive"
	AgentCheckpointUpload              AgentCheckpointPhase = "upload"
	AgentCheckpointTotal               AgentCheckpointPhase = "total"
)

func (phase AgentCheckpointPhase) valid() bool {
	return phase == AgentCheckpointRequestedToQuiesced ||
		phase == AgentCheckpointArchive ||
		phase == AgentCheckpointUpload ||
		phase == AgentCheckpointTotal
}

type AgentCheckpointOutcome string

const (
	AgentCheckpointCommitted AgentCheckpointOutcome = "committed"
	AgentCheckpointSkipped   AgentCheckpointOutcome = "skipped"
	AgentCheckpointFailed    AgentCheckpointOutcome = "failed"
)

func (outcome AgentCheckpointOutcome) valid() bool {
	return outcome == AgentCheckpointCommitted ||
		outcome == AgentCheckpointSkipped ||
		outcome == AgentCheckpointFailed
}

type AgentCheckpointTrigger string

const (
	AgentCheckpointTriggerElapsed    AgentCheckpointTrigger = "elapsed"
	AgentCheckpointTriggerCompletion AgentCheckpointTrigger = "completion"
	AgentCheckpointTriggerExplicit   AgentCheckpointTrigger = "explicit"
	AgentCheckpointTriggerPreemption AgentCheckpointTrigger = "preemption"
)

func (trigger AgentCheckpointTrigger) valid() bool {
	return trigger == AgentCheckpointTriggerElapsed ||
		trigger == AgentCheckpointTriggerCompletion ||
		trigger == AgentCheckpointTriggerExplicit ||
		trigger == AgentCheckpointTriggerPreemption
}

var (
	agentCheckpointDurationHistogram otelmetric.Float64Histogram
	agentCheckpointCaptureCounter    otelmetric.Int64Counter
	agentCheckpointLostWorkHistogram otelmetric.Float64Histogram
)

func InitOTelAgentCheckpoint() {
	meter := otel.Meter("concourse")

	durationHistogram, err := meter.Float64Histogram(
		"concourse.agent.checkpoint.duration",
		otelmetric.WithDescription("Checkpoint capture duration by bounded phase and trigger"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		agentCheckpointDurationHistogram = durationHistogram
	}

	captureCounter, err := meter.Int64Counter(
		"concourse.agent.checkpoint.captures",
		otelmetric.WithDescription("Checkpoint capture attempts by bounded outcome and trigger"),
	)
	if err == nil {
		agentCheckpointCaptureCounter = captureCounter
	}

	lostWorkHistogram, err := meter.Float64Histogram(
		"concourse.agent.checkpoint.lost_work",
		otelmetric.WithDescription("Estimated work lost since the previous committed safe boundary"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		agentCheckpointLostWorkHistogram = lostWorkHistogram
	}
}

func RecordAgentCheckpointDuration(
	ctx context.Context,
	phase AgentCheckpointPhase,
	trigger AgentCheckpointTrigger,
	duration time.Duration,
) {
	if !phase.valid() || !trigger.valid() || duration < 0 ||
		agentCheckpointDurationHistogram == nil {
		return
	}
	agentCheckpointDurationHistogram.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("phase", string(phase)),
			attribute.String("trigger", string(trigger)),
		),
	)
}

func RecordAgentCheckpointOutcome(
	ctx context.Context,
	outcome AgentCheckpointOutcome,
	trigger AgentCheckpointTrigger,
) {
	if !outcome.valid() || !trigger.valid() ||
		agentCheckpointCaptureCounter == nil {
		return
	}
	agentCheckpointCaptureCounter.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("outcome", string(outcome)),
			attribute.String("trigger", string(trigger)),
		),
	)
}

func RecordAgentCheckpointLostWork(
	ctx context.Context,
	trigger AgentCheckpointTrigger,
	duration time.Duration,
) {
	if !trigger.valid() || duration < 0 ||
		agentCheckpointLostWorkHistogram == nil {
		return
	}
	agentCheckpointLostWorkHistogram.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("trigger", string(trigger)),
		),
	)
}
