package exec

import (
	"context"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/runtime"
)

type AgentCheckpointRecoveryOutcome string

const (
	AgentCheckpointRecoverySucceeded            AgentCheckpointRecoveryOutcome = "succeeded"
	AgentCheckpointRecoveryFailed               AgentCheckpointRecoveryOutcome = "failed"
	AgentCheckpointRecoveryManualReviewRequired AgentCheckpointRecoveryOutcome = "manual_review_required"
)

type AgentCheckpointRestoreOutcome string

const (
	AgentCheckpointRestoreSucceeded AgentCheckpointRestoreOutcome = "succeeded"
	AgentCheckpointRestoreFailed    AgentCheckpointRestoreOutcome = "failed"
)

// AgentCheckpointRecoveryMetrics is deliberately bounded. Callers provide
// only closed runtime/checkpoint enums and numeric observations, never run,
// attempt, pod, node, model, or provider-controlled strings.
type AgentCheckpointRecoveryMetrics interface {
	RecordInterruption(context.Context, runtime.InterruptionReason)
	RecordRecovery(context.Context, checkpoint.FallbackMode, AgentCheckpointRecoveryOutcome)
	RecordRestore(context.Context, AgentCheckpointRestoreOutcome, time.Duration)
	RecordAmbiguousEffects(context.Context, int64)
}

func NewOTelAgentCheckpointRecoveryMetrics() AgentCheckpointRecoveryMetrics {
	return otelAgentCheckpointRecoveryMetrics{}
}

type otelAgentCheckpointRecoveryMetrics struct{}

func (otelAgentCheckpointRecoveryMetrics) RecordInterruption(ctx context.Context, reason runtime.InterruptionReason) {
	mapped, ok := otelAgentInterruptionReason(reason)
	if ok {
		metric.RecordAgentInterruption(ctx, mapped)
	}
}

func (otelAgentCheckpointRecoveryMetrics) RecordRecovery(
	ctx context.Context,
	mode checkpoint.FallbackMode,
	outcome AgentCheckpointRecoveryOutcome,
) {
	mappedMode, modeOK := otelAgentRecoveryMode(mode)
	mappedOutcome, outcomeOK := otelAgentRecoveryOutcome(outcome)
	if modeOK && outcomeOK {
		metric.RecordAgentRecovery(ctx, mappedMode, mappedOutcome)
	}
}

func (otelAgentCheckpointRecoveryMetrics) RecordRestore(
	ctx context.Context,
	outcome AgentCheckpointRestoreOutcome,
	duration time.Duration,
) {
	switch outcome {
	case AgentCheckpointRestoreSucceeded:
		metric.RecordAgentRestoreDuration(ctx, metric.AgentRestoreSucceeded, duration)
	case AgentCheckpointRestoreFailed:
		metric.RecordAgentRestoreDuration(ctx, metric.AgentRestoreFailed, duration)
	}
}

func (otelAgentCheckpointRecoveryMetrics) RecordAmbiguousEffects(ctx context.Context, count int64) {
	metric.RecordAgentAmbiguousEffects(ctx, count)
}

func otelAgentInterruptionReason(reason runtime.InterruptionReason) (metric.AgentInterruptionReason, bool) {
	switch reason {
	case runtime.InterruptionPodDeleted:
		return metric.AgentInterruptionPodDeleted, true
	case runtime.InterruptionEvicted:
		return metric.AgentInterruptionEvicted, true
	case runtime.InterruptionNodeLost:
		return metric.AgentInterruptionNodeLost, true
	case runtime.InterruptionPreempted:
		return metric.AgentInterruptionPreempted, true
	default:
		return "", false
	}
}

func otelAgentRecoveryMode(mode checkpoint.FallbackMode) (metric.AgentRecoveryMode, bool) {
	switch mode {
	case checkpoint.FallbackNativeResume:
		return metric.AgentRecoveryNativeResume, true
	case checkpoint.FallbackWorkspaceOnly:
		return metric.AgentRecoveryWorkspaceOnly, true
	case checkpoint.FallbackCheckpointZero:
		return metric.AgentRecoveryCheckpointZero, true
	case checkpoint.FallbackManualReview:
		return metric.AgentRecoveryNotAdmitted, true
	default:
		return "", false
	}
}

func otelAgentRecoveryOutcome(outcome AgentCheckpointRecoveryOutcome) (metric.AgentRecoveryOutcome, bool) {
	switch outcome {
	case AgentCheckpointRecoverySucceeded:
		return metric.AgentRecoverySucceeded, true
	case AgentCheckpointRecoveryFailed:
		return metric.AgentRecoveryFailed, true
	case AgentCheckpointRecoveryManualReviewRequired:
		return metric.AgentRecoveryManualReviewRequired, true
	default:
		return "", false
	}
}

func agentCheckpointRecoveryMetricMode(mode checkpoint.FallbackMode) checkpoint.FallbackMode {
	switch mode {
	case checkpoint.FallbackNativeResume,
		checkpoint.FallbackWorkspaceOnly,
		checkpoint.FallbackCheckpointZero:
		return mode
	default:
		return checkpoint.FallbackManualReview
	}
}

func recordAgentCheckpointRecovery(
	ctx context.Context,
	metrics AgentCheckpointRecoveryMetrics,
	mode checkpoint.FallbackMode,
	outcome AgentCheckpointRecoveryOutcome,
) {
	if metrics != nil {
		metrics.RecordRecovery(ctx, mode, outcome)
	}
}

func recordAgentCheckpointRestore(
	ctx context.Context,
	metrics AgentCheckpointRecoveryMetrics,
	outcome AgentCheckpointRestoreOutcome,
	duration time.Duration,
) {
	if metrics != nil {
		metrics.RecordRestore(ctx, outcome, duration)
	}
}
