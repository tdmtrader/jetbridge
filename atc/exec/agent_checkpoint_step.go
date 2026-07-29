package exec

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/runtime"
)

// AgentCheckpointController is the small durable-lifecycle boundary used by
// AgentStep. The concrete controller keeps all attempt, fence, and checkpoint
// commit authority in ATC; the step only surrounds a real runtime process.
type AgentCheckpointController interface {
	Prepare(context.Context) (checkpoint.Attempt, error)
	MarkRunning(context.Context) (checkpoint.Attempt, error)
	MarkInterrupted(context.Context, checkpoint.Attempt, runtime.InterruptionReason) (checkpoint.Attempt, error)
	CaptureLive(context.Context, CheckpointCaptureTrigger, AgentCheckpointCaptureRequest) (AgentCheckpointCaptureResult, error)
	FinalizeSucceeded(context.Context, AgentCheckpointCaptureRequest) (AgentCheckpointFinalizeResult, error)
}

// AgentCheckpointExecutionFactory supplies a controller for one authenticated
// workflow function. Its implementation is server-owned and is never derived
// from rendered workflow configuration or agent environment.
type AgentCheckpointExecutionFactory interface {
	NewAgentCheckpointExecution(checkpoint.Identity) (AgentCheckpointController, error)
}

// AgentCheckpointExecutionFactoryFunc adapts composition-root closures without
// widening the AgentStep's lifecycle boundary.
type AgentCheckpointExecutionFactoryFunc func(checkpoint.Identity) (AgentCheckpointController, error)

func (factory AgentCheckpointExecutionFactoryFunc) NewAgentCheckpointExecution(identity checkpoint.Identity) (AgentCheckpointController, error) {
	if factory == nil {
		return nil, errors.New("agent checkpoint execution factory is not configured")
	}
	return factory(identity)
}

// AgentCheckpointRecoveryStepController is the pre-launch recovery boundary.
// It exposes only durable launch preparation and exact materialization
// terminalization; AgentStep never selects a source or a mode itself.
type AgentCheckpointRecoveryStepController interface {
	PrepareLaunch(context.Context) (AgentCheckpointRecoveryLaunch, error)
	MarkMaterializationFailed(context.Context, AgentCheckpointRecoveryLaunch) (checkpoint.Attempt, error)
}

type AgentCheckpointRecoveryFactory interface {
	NewAgentCheckpointRecovery(AgentCheckpointImmutableProvenance) (AgentCheckpointRecoveryStepController, error)
}

type AgentCheckpointRecoveryFactoryFunc func(AgentCheckpointImmutableProvenance) (AgentCheckpointRecoveryStepController, error)

func (factory AgentCheckpointRecoveryFactoryFunc) NewAgentCheckpointRecovery(provenance AgentCheckpointImmutableProvenance) (AgentCheckpointRecoveryStepController, error) {
	if factory == nil {
		return nil, errors.New("agent checkpoint recovery factory is not configured")
	}
	return factory(provenance)
}

// AgentCheckpointExplicitIntentSource is an optional, trusted ATC-owned
// source of one explicit capture intent. Implementations must return promptly
// when ctx is cancelled: AgentStep joins this wait before terminal capture so
// a source that ignores cancellation would violate the lifecycle contract.
// This task does not add a public API route; composition may attach an internal
// source later without granting an agent or runtime the ability to manufacture
// checkpoint authority.
type AgentCheckpointExplicitIntentSource interface {
	WaitForAgentCheckpointIntent(context.Context, checkpoint.Identity) error
}

// AgentCheckpointStepConfig contains only server-owned capture policy and
// provider facts. It is deliberately absent from AgentPlan and never exposed
// through container environment.
type AgentCheckpointStepConfig struct {
	Factory              AgentCheckpointExecutionFactory
	RecoveryFactory      AgentCheckpointRecoveryFactory
	Provider             string
	Adapter              provider.Identity
	ElapsedInterval      time.Duration
	MaxArchiveBytes      int64
	MaxArchiveEntries    int64
	ExplicitIntentSource AgentCheckpointExplicitIntentSource
	RecoveryMetrics      AgentCheckpointRecoveryMetrics
}

const agentCheckpointInterruptionTimeout = 5 * time.Second

func (config AgentCheckpointStepConfig) validate() error {
	if config.Factory == nil {
		return errors.New("agent checkpoint execution factory is not configured")
	}
	if strings.TrimSpace(config.Provider) == "" || strings.TrimSpace(config.Provider) != config.Provider {
		return errors.New("agent checkpoint provider is not canonical")
	}
	if err := config.Adapter.Validate(); err != nil {
		return fmt.Errorf("agent checkpoint adapter: %w", err)
	}
	if config.ElapsedInterval <= 0 {
		return errors.New("agent checkpoint elapsed interval must be positive")
	}
	if config.MaxArchiveBytes <= 0 {
		return errors.New("agent checkpoint max archive bytes must be positive")
	}
	if config.RecoveryFactory != nil && config.MaxArchiveEntries <= 0 {
		return errors.New("agent checkpoint max archive entries must be positive")
	}
	return nil
}

// checkpointIdentity selects only the authenticated v3 execution path. A
// configured checkpoint subsystem must never silently broaden to legacy agent
// steps, unpinned images, or renderer-supplied identities.
func (step *AgentStep) checkpointIdentity(runtimeImage, workdir string) (checkpoint.Identity, bool, error) {
	if step.checkpointCapture == nil || !step.plan.Hermetic || step.metadata.WorkflowRunID == nil {
		return checkpoint.Identity{}, false, nil
	}
	if strings.TrimSpace(step.plan.FunctionID) == "" || strings.TrimSpace(step.plan.FunctionID) != step.plan.FunctionID {
		return checkpoint.Identity{}, false, errors.New("agent checkpoint function ID is not canonical")
	}
	if step.plan.RuntimeImage == "" || step.plan.RuntimeImage != runtimeImage {
		return checkpoint.Identity{}, false, errors.New("agent checkpoint runtime image differs from the admitted plan")
	}
	if err := atc.ValidatePinnedOCIImage(step.plan.RuntimeImage); err != nil {
		return checkpoint.Identity{}, false, fmt.Errorf("agent checkpoint runtime image is not immutable: %w", err)
	}
	if strings.TrimSpace(step.plan.Model) == "" || strings.TrimSpace(step.plan.Model) != step.plan.Model {
		return checkpoint.Identity{}, false, errors.New("agent checkpoint model is not canonical")
	}
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return checkpoint.Identity{}, false, errors.New("agent checkpoint working directory must be canonical and absolute")
	}
	if err := step.checkpointCapture.validate(); err != nil {
		return checkpoint.Identity{}, false, err
	}
	identity := checkpoint.Identity{
		WorkflowRunID: step.metadata.WorkflowRunID,
		BuildID:       int64(step.metadata.BuildID),
		PlanID:        string(step.planID),
		FunctionID:    step.plan.FunctionID,
	}
	if err := identity.Validate(); err != nil {
		return checkpoint.Identity{}, false, err
	}
	return identity, true, nil
}

func sameAgentCheckpointIdentity(left, right checkpoint.Identity) bool {
	if left.BuildID != right.BuildID || left.PlanID != right.PlanID || left.FunctionID != right.FunctionID || (left.WorkflowRunID == nil) != (right.WorkflowRunID == nil) {
		return false
	}
	return left.WorkflowRunID == nil || *left.WorkflowRunID == *right.WorkflowRunID
}
