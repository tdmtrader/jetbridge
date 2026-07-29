package exec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/google/uuid"
)

// AgentCheckpointExecution is the pure durable attempt controller. It has no
// timer, provider, or AgentStep ownership; callers invoke its narrow methods
// around actual model execution.
type AgentCheckpointExecutionConfig struct {
	Identity         checkpoint.Identity
	MaxTotalAttempts int
	FenceTTL         time.Duration
	CaptureTimeout   time.Duration
}

type agentCheckpointExecutionAttempts interface {
	Current(context.Context, checkpoint.Identity) (checkpoint.Attempt, bool, error)
	AllocateInitial(context.Context, checkpoint.AllocateInitialAttemptRequest) (checkpoint.Attempt, error)
	Transition(context.Context, checkpoint.TransitionAttemptRequest) (checkpoint.Attempt, error)
	MarkInterrupted(context.Context, checkpoint.MarkAttemptInterruptedRequest) (checkpoint.Attempt, error)
	FinalizeSucceeded(context.Context, checkpoint.FinalizeSucceededRequest) (checkpoint.Attempt, error)
	AcquireFence(context.Context, checkpoint.AcquireAttemptFenceRequest) (checkpoint.AttemptFence, error)
	ReleaseFence(context.Context, checkpoint.ReleaseAttemptFenceRequest) error
}

type checkpointHeadReader interface {
	Latest(context.Context, checkpoint.Identity) (checkpoint.Manifest, bool, error)
}

type AgentCheckpointExecution struct {
	config      AgentCheckpointExecutionConfig
	attempts    agentCheckpointExecutionAttempts
	heads       checkpointHeadReader
	coordinator *AgentCheckpointCapture

	previousSafeAtMu sync.Mutex
	previousSafeAt   time.Time
}

// AgentCheckpointFinalizeResult separates a best-effort terminal capture from
// durable attempt finalization. A capture failure is evidence for callers to
// report, but it must not leave a successfully completed attempt unfinalized.
type AgentCheckpointFinalizeResult struct {
	Attempt      checkpoint.Attempt
	Capture      AgentCheckpointCaptureResult
	CaptureError error
}

func NewAgentCheckpointExecution(config AgentCheckpointExecutionConfig, attempts agentCheckpointExecutionAttempts, heads checkpointHeadReader, capture *AgentCheckpointCapture) (*AgentCheckpointExecution, error) {
	if err := config.Identity.Validate(); err != nil || attempts == nil || config.FenceTTL <= 0 {
		return nil, errors.New("agent checkpoint execution is not configured")
	}
	if config.CaptureTimeout <= 0 {
		config.CaptureTimeout = 30 * time.Second
	}
	return &AgentCheckpointExecution{config: config, attempts: attempts, heads: heads, coordinator: capture}, nil
}

// Prepare allocates the deterministic initial attempt if necessary, then
// durably records that materialization may begin. It deliberately does not
// claim that a runtime process exists yet.
func (controller *AgentCheckpointExecution) Prepare(ctx context.Context) (checkpoint.Attempt, error) {
	attempt, found, err := controller.attempts.Current(ctx, controller.config.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if !found {
		attempt, err = controller.attempts.AllocateInitial(ctx, checkpoint.AllocateInitialAttemptRequest{
			Identity: controller.config.Identity, MaxTotalAttempts: controller.config.MaxTotalAttempts,
			MaterializationID: executionMaterializationID(controller.config.Identity),
		})
		if err != nil {
			return checkpoint.Attempt{}, err
		}
	}
	if attempt.State == checkpoint.AttemptScheduling {
		attempt, err = controller.transition(ctx, attempt, checkpoint.AttemptMaterializing)
		if err != nil {
			return checkpoint.Attempt{}, err
		}
	}
	switch attempt.State {
	case checkpoint.AttemptMaterializing, checkpoint.AttemptRunning, checkpoint.AttemptFinalizing, checkpoint.AttemptSucceeded:
		return attempt, nil
	default:
		return checkpoint.Attempt{}, fmt.Errorf("agent checkpoint attempt is not preparable: %s", attempt.State)
	}
}

// MarkRunning is called only after the caller has attached to a concrete
// runtime process. It is idempotent for re-entry after the durable transition.
func (controller *AgentCheckpointExecution) MarkRunning(ctx context.Context) (checkpoint.Attempt, error) {
	attempt, found, err := controller.attempts.Current(ctx, controller.config.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if !found {
		return checkpoint.Attempt{}, errors.New("agent checkpoint running transition requires a current attempt")
	}
	if attempt.State == checkpoint.AttemptRunning {
		return attempt, nil
	}
	if attempt.State != checkpoint.AttemptMaterializing {
		return checkpoint.Attempt{}, fmt.Errorf("agent checkpoint attempt is not materializing: %s", attempt.State)
	}
	return controller.transition(ctx, attempt, checkpoint.AttemptRunning)
}

func (controller *AgentCheckpointExecution) MarkInterrupted(ctx context.Context, attempt checkpoint.Attempt, reason runtime.InterruptionReason) (checkpoint.Attempt, error) {
	mapped, err := checkpointInterruptionReason(reason)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	return controller.attempts.MarkInterrupted(ctx, checkpoint.MarkAttemptInterruptedRequest{Identity: controller.config.Identity, ExecutionAttempt: attempt.ExecutionAttempt, Reason: mapped})
}

func (controller *AgentCheckpointExecution) CaptureLive(ctx context.Context, trigger CheckpointCaptureTrigger, request AgentCheckpointCaptureRequest) (AgentCheckpointCaptureResult, error) {
	if trigger != CheckpointCaptureTriggerElapsed && trigger != CheckpointCaptureTriggerExplicit && trigger != CheckpointCaptureTriggerPreemption {
		return AgentCheckpointCaptureResult{}, errors.New("live checkpoint trigger is invalid")
	}
	attempt, found, err := controller.attempts.Current(ctx, controller.config.Identity)
	if err != nil {
		return AgentCheckpointCaptureResult{}, err
	}
	if !found || attempt.State != checkpoint.AttemptRunning {
		return AgentCheckpointCaptureResult{}, errors.New("live checkpoint capture requires a running attempt")
	}
	return controller.capture(ctx, attempt, trigger, request)
}

func (controller *AgentCheckpointExecution) CaptureTerminal(ctx context.Context, request AgentCheckpointCaptureRequest) (AgentCheckpointCaptureResult, error) {
	attempt, found, err := controller.attempts.Current(ctx, controller.config.Identity)
	if err != nil {
		return AgentCheckpointCaptureResult{}, err
	}
	if !found || attempt.State != checkpoint.AttemptFinalizing {
		return AgentCheckpointCaptureResult{}, errors.New("agent checkpoint terminal capture requires a finalizing attempt")
	}
	return controller.capture(ctx, attempt, CheckpointCaptureTriggerCompletion, request)
}

func (controller *AgentCheckpointExecution) capture(ctx context.Context, attempt checkpoint.Attempt, trigger CheckpointCaptureTrigger, request AgentCheckpointCaptureRequest) (AgentCheckpointCaptureResult, error) {
	if controller.coordinator == nil || controller.heads == nil {
		return AgentCheckpointCaptureResult{}, errors.New("agent checkpoint capture is unavailable")
	}
	request.Trigger = trigger
	request.Provenance.Identity = controller.config.Identity
	request.Provenance.ExecutionAttempt = attempt.ExecutionAttempt
	request.FenceTTL = controller.config.FenceTTL
	request.FenceToken = uuid.NewString()
	controller.previousSafeAtMu.Lock()
	request.Provenance.PreviousSafeAt = controller.previousSafeAt
	controller.previousSafeAtMu.Unlock()
	if head, found, latestErr := controller.heads.Latest(ctx, controller.config.Identity); latestErr != nil {
		return AgentCheckpointCaptureResult{}, latestErr
	} else if found {
		request.Provenance.PreviousSafeAt = head.SafeAt
	}
	captureCtx, cancel := context.WithTimeout(ctx, controller.config.CaptureTimeout)
	defer cancel()
	result, err := controller.coordinator.Capture(captureCtx, request)
	if err == nil && result.Status == CheckpointCaptureCommitted {
		controller.previousSafeAtMu.Lock()
		controller.previousSafeAt = result.Manifest.SafeAt
		controller.previousSafeAtMu.Unlock()
	}
	return result, err
}

// FinalizeSucceeded transitions a running attempt into finalizing, performs a
// completion capture under the terminal runtime interface, and terminalizes
// the attempt. It deliberately releases every short-lived fence before model
// work can resume; this controller never owns the model execution itself.
func (controller *AgentCheckpointExecution) FinalizeSucceeded(ctx context.Context, request AgentCheckpointCaptureRequest) (AgentCheckpointFinalizeResult, error) {
	attempt, found, err := controller.attempts.Current(ctx, controller.config.Identity)
	if err != nil {
		return AgentCheckpointFinalizeResult{}, err
	}
	if !found {
		return AgentCheckpointFinalizeResult{}, errors.New("agent checkpoint finalization requires a current attempt")
	}
	if attempt.State == checkpoint.AttemptRunning {
		attempt, err = controller.transition(ctx, attempt, checkpoint.AttemptFinalizing)
		if err != nil {
			return AgentCheckpointFinalizeResult{}, err
		}
	}
	if attempt.State != checkpoint.AttemptFinalizing {
		return AgentCheckpointFinalizeResult{}, fmt.Errorf("agent checkpoint attempt is not finalizing: %s", attempt.State)
	}

	result := AgentCheckpointFinalizeResult{Attempt: attempt}
	result.Capture, result.CaptureError = controller.capture(ctx, attempt, CheckpointCaptureTriggerCompletion, request)

	fence, release, err := controller.fence(ctx, attempt.ExecutionAttempt)
	if err != nil {
		return result, err
	}
	completed, err := controller.attempts.FinalizeSucceeded(ctx, checkpoint.FinalizeSucceededRequest{
		Identity: controller.config.Identity, ExecutionAttempt: attempt.ExecutionAttempt, Fence: fence,
	})
	releaseErr := release()
	if err != nil {
		return result, errors.Join(err, releaseErr)
	}
	if releaseErr != nil {
		return result, releaseErr
	}
	result.Attempt = completed
	return result, nil
}

func (controller *AgentCheckpointExecution) transition(ctx context.Context, attempt checkpoint.Attempt, next checkpoint.AttemptState) (checkpoint.Attempt, error) {
	fence, release, err := controller.fence(ctx, attempt.ExecutionAttempt)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	transitioned, transitionErr := controller.attempts.Transition(ctx, checkpoint.TransitionAttemptRequest{Identity: controller.config.Identity, ExecutionAttempt: attempt.ExecutionAttempt, ExpectedState: attempt.State, State: next, Fence: fence})
	return transitioned, errors.Join(transitionErr, release())
}

func (controller *AgentCheckpointExecution) fence(ctx context.Context, executionAttempt int) (checkpoint.FenceClaim, func() error, error) {
	token := uuid.NewString()
	fence, err := controller.attempts.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{Identity: controller.config.Identity, ExecutionAttempt: executionAttempt, Token: token, TTL: controller.config.FenceTTL})
	if err != nil {
		return checkpoint.FenceClaim{}, func() error { return nil }, err
	}
	release := func() error {
		releaseCtx, cancel := context.WithTimeout(context.Background(), checkpointCaptureCleanupTimeout)
		defer cancel()
		return controller.attempts.ReleaseFence(releaseCtx, checkpoint.ReleaseAttemptFenceRequest{
			Identity: controller.config.Identity, ExecutionAttempt: executionAttempt, Token: token,
		})
	}
	if fence.FenceClaim.ExecutionAttempt != executionAttempt || fence.FenceClaim.Token != token {
		_ = release()
		return checkpoint.FenceClaim{}, func() error { return nil }, errors.New("agent checkpoint execution acquired mismatched fence")
	}
	return fence.FenceClaim, release, nil
}

func executionMaterializationID(identity checkpoint.Identity) string {
	value := fmt.Sprintf("%d:%s:%s", identity.BuildID, identity.PlanID, identity.FunctionID)
	digest := sha256.Sum256([]byte(value))
	return "agent-checkpoint-" + hex.EncodeToString(digest[:])
}

func checkpointInterruptionReason(reason runtime.InterruptionReason) (checkpoint.InterruptionReason, error) {
	switch reason {
	case runtime.InterruptionPodDeleted:
		return checkpoint.InterruptionPodDeleted, nil
	case runtime.InterruptionEvicted:
		return checkpoint.InterruptionEvicted, nil
	case runtime.InterruptionNodeLost:
		return checkpoint.InterruptionNodeLost, nil
	case runtime.InterruptionPreempted:
		return checkpoint.InterruptionPreempted, nil
	default:
		return "", errors.New("unknown runtime interruption reason")
	}
}
