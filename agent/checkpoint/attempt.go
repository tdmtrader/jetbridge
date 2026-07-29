package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const DefaultMaxTotalAttempts = 3

var (
	ErrAttemptLimit = errors.New("checkpoint: execution attempt limit reached")
	ErrStaleFence   = errors.New("checkpoint: stale attempt fence")
)

// AttemptState is the durable lifecycle of one process/container attempt.
// Interrupted attempts are immutable evidence; recovery always allocates a
// new row rather than moving an interrupted row back to running.
type AttemptState string

const (
	AttemptScheduling    AttemptState = "scheduling"
	AttemptMaterializing AttemptState = "materializing"
	AttemptRunning       AttemptState = "running"
	AttemptInterrupted   AttemptState = "interrupted"
	AttemptFinalizing    AttemptState = "finalizing"
	AttemptSucceeded     AttemptState = "succeeded"
	AttemptManualReview  AttemptState = "manual_review_required"
)

func (state AttemptState) Validate() error {
	switch state {
	case AttemptScheduling, AttemptMaterializing, AttemptRunning, AttemptInterrupted,
		AttemptFinalizing, AttemptSucceeded, AttemptManualReview:
		return nil
	default:
		return fmt.Errorf("checkpoint: invalid execution attempt state %q", state)
	}
}

func (state AttemptState) Terminal() bool {
	return state == AttemptSucceeded || state == AttemptManualReview
}

// CanTransitionTo is intentionally closed. Retrying a successful transition
// is handled idempotently by the store and is not a second state transition.
func (state AttemptState) CanTransitionTo(next AttemptState) bool {
	switch state {
	case AttemptScheduling:
		return next == AttemptMaterializing || next == AttemptRunning ||
			next == AttemptInterrupted || next == AttemptManualReview
	case AttemptMaterializing:
		return next == AttemptRunning || next == AttemptInterrupted || next == AttemptManualReview
	case AttemptRunning:
		return next == AttemptInterrupted || next == AttemptFinalizing || next == AttemptManualReview
	case AttemptInterrupted:
		return next == AttemptManualReview
	case AttemptFinalizing:
		return next == AttemptInterrupted || next == AttemptSucceeded || next == AttemptManualReview
	default:
		return false
	}
}

// InterruptionReason is deliberately duplicated at the durable checkpoint
// boundary so the agent package does not depend on the ATC runtime package.
// Runtime callers must explicitly translate their typed lifecycle reason.
type InterruptionReason string

const (
	InterruptionPodDeleted InterruptionReason = "pod_deleted"
	InterruptionEvicted    InterruptionReason = "evicted"
	InterruptionNodeLost   InterruptionReason = "node_lost"
	InterruptionPreempted  InterruptionReason = "preempted"
)

func (reason InterruptionReason) Validate() error {
	switch reason {
	case InterruptionPodDeleted, InterruptionEvicted, InterruptionNodeLost, InterruptionPreempted:
		return nil
	default:
		return fmt.Errorf("checkpoint: invalid interruption reason %q", reason)
	}
}

// FenceClaim is the authority presented to a checkpoint mutation. Expiration
// is never caller supplied: PostgreSQL compares its own clock to the durable
// lease row in the same transaction as the mutation.
type FenceClaim struct {
	ExecutionAttempt int
	Token            string
}

func (claim FenceClaim) Validate() error {
	if claim.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: fence execution attempt must be positive")
	}
	return validateFenceToken(claim.Token)
}

type AttemptFence struct {
	FenceClaim
	ExpiresAt time.Time
}

type Attempt struct {
	ID                         int64
	Identity                   Identity
	ExecutionAttempt           int
	MaxTotalAttempts           int
	State                      AttemptState
	Current                    bool
	MaterializationID          string
	SourceExecutionAttempt     int
	SourceCheckpointID         *int64
	SourceCheckpointGeneration int
	Mode                       FallbackMode
	SourceInterruptionReason   InterruptionReason
	InterruptionReason         InterruptionReason
	Fence                      *AttemptFence
	CreatedAt                  time.Time
	UpdatedAt                  time.Time
	InterruptedAt              *time.Time
	TerminalAt                 *time.Time
}

func (attempt Attempt) Clone() Attempt {
	attempt.Identity = attempt.Identity.Clone()
	if attempt.SourceCheckpointID != nil {
		value := *attempt.SourceCheckpointID
		attempt.SourceCheckpointID = &value
	}
	if attempt.Fence != nil {
		value := *attempt.Fence
		attempt.Fence = &value
	}
	if attempt.InterruptedAt != nil {
		value := *attempt.InterruptedAt
		attempt.InterruptedAt = &value
	}
	if attempt.TerminalAt != nil {
		value := *attempt.TerminalAt
		attempt.TerminalAt = &value
	}
	return attempt
}

type AllocateInitialAttemptRequest struct {
	Identity          Identity
	MaxTotalAttempts  int
	MaterializationID string
}

func (request AllocateInitialAttemptRequest) Clone() AllocateInitialAttemptRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request AllocateInitialAttemptRequest) EffectiveMaxTotalAttempts() int {
	if request.MaxTotalAttempts == 0 {
		return DefaultMaxTotalAttempts
	}
	return request.MaxTotalAttempts
}

func (request AllocateInitialAttemptRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.MaxTotalAttempts < 0 {
		return fmt.Errorf("checkpoint: max total attempts cannot be negative")
	}
	if request.EffectiveMaxTotalAttempts() < 1 {
		return fmt.Errorf("checkpoint: max total attempts must be positive")
	}
	return validateMaterializationID(request.MaterializationID)
}

type MarkAttemptInterruptedRequest struct {
	Identity         Identity
	ExecutionAttempt int
	Reason           InterruptionReason
}

func (request MarkAttemptInterruptedRequest) Clone() MarkAttemptInterruptedRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request MarkAttemptInterruptedRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: interrupted execution attempt must be positive")
	}
	return request.Reason.Validate()
}

type BeginRecoveryRequest struct {
	Identity                   Identity
	SourceExecutionAttempt     int
	SourceCheckpointID         *int64
	SourceCheckpointGeneration int
	Mode                       FallbackMode
	Reason                     InterruptionReason
	MaterializationID          string
}

func (request BeginRecoveryRequest) Clone() BeginRecoveryRequest {
	request.Identity = request.Identity.Clone()
	if request.SourceCheckpointID != nil {
		value := *request.SourceCheckpointID
		request.SourceCheckpointID = &value
	}
	return request
}

func (request BeginRecoveryRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.SourceExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: recovery source attempt must be positive")
	}
	if err := request.Reason.Validate(); err != nil {
		return err
	}
	if err := validateMaterializationID(request.MaterializationID); err != nil {
		return err
	}
	switch request.Mode {
	case FallbackNativeResume, FallbackWorkspaceOnly:
		if request.SourceCheckpointID == nil || *request.SourceCheckpointID <= 0 ||
			request.SourceCheckpointGeneration <= 0 {
			return fmt.Errorf("checkpoint: nonzero recovery requires a source checkpoint")
		}
	case FallbackCheckpointZero:
		if request.SourceCheckpointID != nil || request.SourceCheckpointGeneration != 0 {
			return fmt.Errorf("checkpoint: checkpoint-zero recovery cannot name a source checkpoint")
		}
	default:
		return fmt.Errorf("checkpoint: recovery mode %q cannot allocate an automatic attempt", request.Mode)
	}
	return nil
}

type TransitionAttemptRequest struct {
	Identity         Identity
	ExecutionAttempt int
	ExpectedState    AttemptState
	State            AttemptState
	Fence            FenceClaim
}

// FinalizeSucceededRequest is the fenced authority required to durably
// terminalize the current finalizing attempt.
type FinalizeSucceededRequest struct {
	Identity         Identity
	ExecutionAttempt int
	Fence            FenceClaim
}

func (request FinalizeSucceededRequest) Clone() FinalizeSucceededRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request FinalizeSucceededRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt <= 0 || request.ExecutionAttempt != request.Fence.ExecutionAttempt {
		return fmt.Errorf("checkpoint: succeeded attempt does not match fence")
	}
	return request.Fence.Validate()
}

type MarkAttemptManualReviewRequest struct {
	Identity         Identity
	ExecutionAttempt int
	ExpectedState    AttemptState
	// MaterializationID fences post-allocation recovery failure handling. It
	// is required for scheduling/materializing replacements and absent for the
	// original interrupted-source manual-review path.
	MaterializationID string
}

func (request MarkAttemptManualReviewRequest) Clone() MarkAttemptManualReviewRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request MarkAttemptManualReviewRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: manual-review execution attempt must be positive")
	}
	if err := request.ExpectedState.Validate(); err != nil {
		return err
	}
	if request.ExpectedState == AttemptInterrupted {
		if request.MaterializationID != "" {
			return fmt.Errorf("checkpoint: interrupted manual review cannot name materialization")
		}
		return nil
	}
	if request.ExpectedState != AttemptScheduling && request.ExpectedState != AttemptMaterializing {
		return fmt.Errorf(
			"checkpoint: manual review state %q is not terminalizable",
			request.ExpectedState,
		)
	}
	return validateMaterializationID(request.MaterializationID)
}

func (request TransitionAttemptRequest) Clone() TransitionAttemptRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request TransitionAttemptRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt <= 0 || request.ExecutionAttempt != request.Fence.ExecutionAttempt {
		return fmt.Errorf("checkpoint: transition attempt does not match fence")
	}
	if err := request.ExpectedState.Validate(); err != nil {
		return err
	}
	if err := request.State.Validate(); err != nil {
		return err
	}
	if !request.ExpectedState.CanTransitionTo(request.State) {
		return fmt.Errorf("checkpoint: invalid attempt transition %q -> %q", request.ExpectedState, request.State)
	}
	return request.Fence.Validate()
}

type AcquireAttemptFenceRequest struct {
	Identity         Identity
	ExecutionAttempt int
	Token            string
	TTL              time.Duration
}

func (request AcquireAttemptFenceRequest) Clone() AcquireAttemptFenceRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request AcquireAttemptFenceRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: fence execution attempt must be positive")
	}
	if err := validateFenceToken(request.Token); err != nil {
		return err
	}
	if request.TTL <= 0 {
		return fmt.Errorf("checkpoint: fence TTL must be positive")
	}
	return nil
}

type ReleaseAttemptFenceRequest struct {
	Identity         Identity
	ExecutionAttempt int
	Token            string
}

func (request ReleaseAttemptFenceRequest) Clone() ReleaseAttemptFenceRequest {
	request.Identity = request.Identity.Clone()
	return request
}

func (request ReleaseAttemptFenceRequest) Validate() error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if request.ExecutionAttempt <= 0 {
		return fmt.Errorf("checkpoint: fence execution attempt must be positive")
	}
	return validateFenceToken(request.Token)
}

// AttemptStore serializes attempt selection, lifecycle transitions, and
// checkpoint mutation leases through PostgreSQL.
type AttemptStore interface {
	AllocateInitial(context.Context, AllocateInitialAttemptRequest) (Attempt, error)
	Current(context.Context, Identity) (Attempt, bool, error)
	MarkInterrupted(context.Context, MarkAttemptInterruptedRequest) (Attempt, error)
	MarkManualReview(context.Context, MarkAttemptManualReviewRequest) (Attempt, error)
	BeginRecovery(context.Context, BeginRecoveryRequest) (Attempt, error)
	Transition(context.Context, TransitionAttemptRequest) (Attempt, error)
	FinalizeSucceeded(context.Context, FinalizeSucceededRequest) (Attempt, error)
	AcquireFence(context.Context, AcquireAttemptFenceRequest) (AttemptFence, error)
	ReleaseFence(context.Context, ReleaseAttemptFenceRequest) error
}

func validateFenceToken(token string) error {
	parsed, err := uuid.Parse(token)
	if err != nil || parsed.String() != token {
		return fmt.Errorf("checkpoint: fence token must be a canonical UUID")
	}
	return nil
}

func validateMaterializationID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("checkpoint: materialization ID is required")
	}
	if len(id) > 256 {
		return fmt.Errorf("checkpoint: materialization ID exceeds 256 bytes")
	}
	return nil
}
