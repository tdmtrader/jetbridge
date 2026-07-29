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

type AgentInterruptionReason string

const (
	AgentInterruptionPodDeleted AgentInterruptionReason = "pod_deleted"
	AgentInterruptionEvicted    AgentInterruptionReason = "evicted"
	AgentInterruptionNodeLost   AgentInterruptionReason = "node_lost"
	AgentInterruptionPreempted  AgentInterruptionReason = "preempted"
)

func (reason AgentInterruptionReason) valid() bool {
	return reason == AgentInterruptionPodDeleted ||
		reason == AgentInterruptionEvicted ||
		reason == AgentInterruptionNodeLost ||
		reason == AgentInterruptionPreempted
}

type AgentRecoveryMode string

const (
	AgentRecoveryNativeResume   AgentRecoveryMode = "native_resume"
	AgentRecoveryWorkspaceOnly  AgentRecoveryMode = "workspace_only"
	AgentRecoveryCheckpointZero AgentRecoveryMode = "checkpoint_zero"
	AgentRecoveryNotAdmitted    AgentRecoveryMode = "not_admitted"
)

func (mode AgentRecoveryMode) valid() bool {
	return mode == AgentRecoveryNativeResume ||
		mode == AgentRecoveryWorkspaceOnly ||
		mode == AgentRecoveryCheckpointZero ||
		mode == AgentRecoveryNotAdmitted
}

type AgentRecoveryOutcome string

const (
	AgentRecoverySucceeded            AgentRecoveryOutcome = "succeeded"
	AgentRecoveryFailed               AgentRecoveryOutcome = "failed"
	AgentRecoveryManualReviewRequired AgentRecoveryOutcome = "manual_review_required"
)

func (outcome AgentRecoveryOutcome) valid() bool {
	return outcome == AgentRecoverySucceeded ||
		outcome == AgentRecoveryFailed ||
		outcome == AgentRecoveryManualReviewRequired
}

type AgentRestoreOutcome string

const (
	AgentRestoreSucceeded AgentRestoreOutcome = "succeeded"
	AgentRestoreFailed    AgentRestoreOutcome = "failed"
)

func (outcome AgentRestoreOutcome) valid() bool {
	return outcome == AgentRestoreSucceeded || outcome == AgentRestoreFailed
}

var (
	agentCheckpointDurationHistogram      otelmetric.Float64Histogram
	agentCheckpointCaptureCounter         otelmetric.Int64Counter
	agentCheckpointLostWorkHistogram      otelmetric.Float64Histogram
	agentCheckpointRetainedBytesHistogram otelmetric.Int64Histogram
	agentInterruptionCounter              otelmetric.Int64Counter
	agentRecoveryCounter                  otelmetric.Int64Counter
	agentRestoreDurationHistogram         otelmetric.Float64Histogram
	agentAmbiguousEffectsCounter          otelmetric.Int64Counter
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

	retainedBytesHistogram, err := meter.Int64Histogram(
		"concourse.agent.checkpoint.retained_bytes",
		otelmetric.WithDescription("Uncompressed bytes retained by each committed checkpoint archive"),
		otelmetric.WithUnit("By"),
	)
	if err == nil {
		agentCheckpointRetainedBytesHistogram = retainedBytesHistogram
	}

	interruptionCounter, err := meter.Int64Counter(
		"concourse.agent.interruptions",
		otelmetric.WithDescription("Agent execution interruptions by bounded lifecycle reason"),
	)
	if err == nil {
		agentInterruptionCounter = interruptionCounter
	}

	recoveryCounter, err := meter.Int64Counter(
		"concourse.agent.recovery.attempts",
		otelmetric.WithDescription("Agent recovery dispositions by bounded mode and outcome"),
	)
	if err == nil {
		agentRecoveryCounter = recoveryCounter
	}

	restoreDurationHistogram, err := meter.Float64Histogram(
		"concourse.agent.recovery.restore.duration",
		otelmetric.WithDescription("Agent checkpoint restore duration by bounded outcome"),
		otelmetric.WithUnit("s"),
	)
	if err == nil {
		agentRestoreDurationHistogram = restoreDurationHistogram
	}

	ambiguousEffectsCounter, err := meter.Int64Counter(
		"concourse.agent.recovery.ambiguous_effects",
		otelmetric.WithDescription("Unsafe or incomplete external effects that force manual review"),
	)
	if err == nil {
		agentAmbiguousEffectsCounter = ambiguousEffectsCounter
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

func RecordAgentCheckpointRetainedBytes(
	ctx context.Context,
	trigger AgentCheckpointTrigger,
	bytes int64,
) {
	if !trigger.valid() || bytes < 0 ||
		agentCheckpointRetainedBytesHistogram == nil {
		return
	}
	agentCheckpointRetainedBytesHistogram.Record(ctx, bytes,
		otelmetric.WithAttributes(
			attribute.String("trigger", string(trigger)),
		),
	)
}

func RecordAgentInterruption(
	ctx context.Context,
	reason AgentInterruptionReason,
) {
	if !reason.valid() || agentInterruptionCounter == nil {
		return
	}
	agentInterruptionCounter.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("reason", string(reason)),
		),
	)
}

// RecordAgentRecovery records a recovery disposition. "succeeded" means a
// fresh recovery process crossed its pre-launch restore gate; it does not
// describe the eventual agent-session result.
func RecordAgentRecovery(
	ctx context.Context,
	mode AgentRecoveryMode,
	outcome AgentRecoveryOutcome,
) {
	if !mode.valid() || !outcome.valid() || agentRecoveryCounter == nil {
		return
	}
	agentRecoveryCounter.Add(ctx, 1,
		otelmetric.WithAttributes(
			attribute.String("mode", string(mode)),
			attribute.String("outcome", string(outcome)),
		),
	)
}

func RecordAgentRestoreDuration(
	ctx context.Context,
	outcome AgentRestoreOutcome,
	duration time.Duration,
) {
	if !outcome.valid() || duration < 0 ||
		agentRestoreDurationHistogram == nil {
		return
	}
	agentRestoreDurationHistogram.Record(ctx, duration.Seconds(),
		otelmetric.WithAttributes(
			attribute.String("outcome", string(outcome)),
		),
	)
}

func RecordAgentAmbiguousEffects(ctx context.Context, count int64) {
	if count <= 0 || agentAmbiguousEffectsCounter == nil {
		return
	}
	agentAmbiguousEffectsCounter.Add(ctx, count)
}
