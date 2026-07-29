package exec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/provider"
	"github.com/concourse/concourse/agent/runner"
	"github.com/concourse/concourse/atc/runtime"
)

// AgentCheckpointRecoveryAuthority is static server configuration. It is
// intentionally not derived from workflow YAML or provider stream output.
type AgentCheckpointRecoveryAuthority struct {
	Provider              string
	Adapter               provider.Identity
	RuntimeImage          string
	CompleteEffectJournal bool
	Capabilities          checkpoint.Capabilities
	RecoveryProof         *provider.RecoveryProof
}

type AgentCheckpointRecoveryConfig struct {
	Provenance        AgentCheckpointImmutableProvenance
	Authorities       []AgentCheckpointRecoveryAuthority
	MaxArchiveBytes   int64
	MaxArchiveEntries int64
	Metrics           AgentCheckpointRecoveryMetrics
}

type agentCheckpointRecoveryAttempts interface {
	Current(context.Context, checkpoint.Identity) (checkpoint.Attempt, bool, error)
	BeginRecovery(context.Context, checkpoint.BeginRecoveryRequest) (checkpoint.Attempt, error)
	MarkManualReview(context.Context, checkpoint.MarkAttemptManualReviewRequest) (checkpoint.Attempt, error)
}

type agentCheckpointRecoverySources interface {
	Latest(context.Context, checkpoint.Identity) (checkpoint.Manifest, bool, error)
	LoadRetainedRecoverySource(context.Context, checkpoint.Identity, int64, int) (checkpoint.Manifest, error)
}

type agentCheckpointRecoveryJournal interface {
	ListEffects(context.Context, checkpoint.Identity, int) ([]checkpoint.Effect, error)
}

type AgentCheckpointRecoveryController struct {
	config   AgentCheckpointRecoveryConfig
	attempts agentCheckpointRecoveryAttempts
	sources  agentCheckpointRecoverySources
	journal  agentCheckpointRecoveryJournal
}

type AgentCheckpointRecoveryLaunch struct {
	Attempt      checkpoint.Attempt
	Restore      *runtime.CheckpointRestoreDescriptor
	RecoverySpec string
	ManualReview bool
}

var ErrAgentCheckpointNotRecovery = errors.New("current attempt is not a recovery attempt")

func NewAgentCheckpointRecoveryController(config AgentCheckpointRecoveryConfig, attempts agentCheckpointRecoveryAttempts, sources agentCheckpointRecoverySources, journal agentCheckpointRecoveryJournal) (*AgentCheckpointRecoveryController, error) {
	if err := config.Provenance.Validate(); err != nil || attempts == nil || sources == nil || journal == nil || config.MaxArchiveBytes <= 0 || config.MaxArchiveEntries <= 0 {
		return nil, errors.New("agent checkpoint recovery is not configured")
	}
	return &AgentCheckpointRecoveryController{config: config, attempts: attempts, sources: sources, journal: journal}, nil
}

// PrepareLaunch derives every recovery input from durable state. It accepts no
// source, mode, archive path, or provider-selected recovery data.
func (controller *AgentCheckpointRecoveryController) PrepareLaunch(ctx context.Context) (AgentCheckpointRecoveryLaunch, error) {
	attempt, found, err := controller.attempts.Current(ctx, controller.config.Provenance.Identity)
	if err != nil {
		return AgentCheckpointRecoveryLaunch{}, err
	}
	if !found {
		return AgentCheckpointRecoveryLaunch{}, ErrAgentCheckpointNotRecovery
	}
	switch attempt.State {
	case checkpoint.AttemptInterrupted:
		return controller.prepareInterrupted(ctx, attempt)
	case checkpoint.AttemptScheduling, checkpoint.AttemptMaterializing, checkpoint.AttemptRunning:
		if attempt.SourceExecutionAttempt <= 0 {
			return AgentCheckpointRecoveryLaunch{}, ErrAgentCheckpointNotRecovery
		}
		return controller.prepareReentry(ctx, attempt)
	default:
		return AgentCheckpointRecoveryLaunch{}, fmt.Errorf("agent checkpoint recovery attempt is not launchable: %s", attempt.State)
	}
}

func (controller *AgentCheckpointRecoveryController) prepareInterrupted(ctx context.Context, source checkpoint.Attempt) (AgentCheckpointRecoveryLaunch, error) {
	manifest, found, err := controller.sources.Latest(ctx, controller.config.Provenance.Identity)
	if err != nil {
		return AgentCheckpointRecoveryLaunch{}, err
	}
	if !found {
		if _, err := controller.cleanJournal(ctx, source.ExecutionAttempt, controller.authority()); err != nil {
			return controller.manualSource(ctx, source, err)
		}
		return controller.allocate(ctx, source, checkpoint.Manifest{}, checkpoint.FallbackCheckpointZero)
	}
	if manifest.Archive == nil {
		return controller.manualSource(ctx, source, errors.New("latest checkpoint does not contain a recovery archive"))
	}
	mode, err := controller.decide(ctx, source, manifest)
	if err != nil {
		return controller.manualSource(ctx, source, err)
	}
	return controller.allocate(ctx, source, manifest, mode)
}

func (controller *AgentCheckpointRecoveryController) allocate(ctx context.Context, source checkpoint.Attempt, manifest checkpoint.Manifest, mode checkpoint.FallbackMode) (AgentCheckpointRecoveryLaunch, error) {
	request := checkpoint.BeginRecoveryRequest{
		Identity: controller.config.Provenance.Identity, SourceExecutionAttempt: source.ExecutionAttempt,
		Mode: mode, Reason: source.InterruptionReason,
		MaterializationID: recoveryMaterializationID(controller.config.Provenance.Identity, source.ExecutionAttempt, manifest.CheckpointID, manifest.Generation, mode),
	}
	if mode != checkpoint.FallbackCheckpointZero {
		id := manifest.CheckpointID
		request.SourceCheckpointID, request.SourceCheckpointGeneration = &id, manifest.Generation
	}
	attempt, err := controller.attempts.BeginRecovery(ctx, request)
	if errors.Is(err, checkpoint.ErrAttemptLimit) {
		return controller.manualSource(ctx, source, err)
	}
	if err != nil {
		return AgentCheckpointRecoveryLaunch{}, err
	}
	return controller.launchFor(ctx, attempt, manifest)
}

func (controller *AgentCheckpointRecoveryController) prepareReentry(ctx context.Context, attempt checkpoint.Attempt) (AgentCheckpointRecoveryLaunch, error) {
	if attempt.Mode == checkpoint.FallbackCheckpointZero {
		if attempt.SourceCheckpointID != nil || attempt.SourceCheckpointGeneration != 0 {
			return controller.manualReplacement(ctx, attempt, errors.New("checkpoint-zero recovery has a source checkpoint"))
		}
		if _, err := controller.cleanJournal(ctx, attempt.SourceExecutionAttempt, controller.authority()); err != nil {
			return controller.manualReplacement(ctx, attempt, err)
		}
		return controller.launchFor(ctx, attempt, checkpoint.Manifest{})
	}
	if attempt.SourceCheckpointID == nil || attempt.SourceCheckpointGeneration <= 0 {
		return controller.manualReplacement(ctx, attempt, errors.New("recovery attempt has no frozen source"))
	}
	manifest, err := controller.sources.LoadRetainedRecoverySource(ctx, controller.config.Provenance.Identity, *attempt.SourceCheckpointID, attempt.SourceCheckpointGeneration)
	if err != nil {
		return controller.manualReplacement(ctx, attempt, err)
	}
	if err := controller.validateFrozen(ctx, attempt, manifest); err != nil {
		return controller.manualReplacement(ctx, attempt, err)
	}
	return controller.launchFor(ctx, attempt, manifest)
}

func (controller *AgentCheckpointRecoveryController) launchFor(ctx context.Context, attempt checkpoint.Attempt, manifest checkpoint.Manifest) (AgentCheckpointRecoveryLaunch, error) {
	authority := controller.authority()
	if authority == nil {
		return AgentCheckpointRecoveryLaunch{}, errors.New("no static recovery authority for admitted provenance")
	}
	sessionID := ""
	if attempt.Mode == checkpoint.FallbackNativeResume {
		sessionID = manifest.SessionID
	}
	spec, err := runner.EncodeRecoverySpec(runner.RecoverySpec{Mode: attempt.Mode, Adapter: authority.Adapter, ExecutionAttempt: attempt.ExecutionAttempt, CheckpointGeneration: manifest.Generation, TranscriptCursor: manifest.TranscriptCursor, CompletedToolCallIDs: canonicalToolIDs(manifest.CompletedToolCallIDs), SessionID: sessionID})
	if err != nil {
		return AgentCheckpointRecoveryLaunch{}, err
	}
	launch := AgentCheckpointRecoveryLaunch{Attempt: attempt, RecoverySpec: spec}
	if attempt.Mode != checkpoint.FallbackCheckpointZero {
		if manifest.Archive == nil {
			return AgentCheckpointRecoveryLaunch{}, errors.New("nonzero recovery source has no archive")
		}
		launch.Restore = &runtime.CheckpointRestoreDescriptor{Archive: checkpoint.Archive{Ref: *manifest.Archive}, MaterializationID: attempt.MaterializationID, MaxBytes: controller.config.MaxArchiveBytes, MaxEntries: controller.config.MaxArchiveEntries}
	}
	return launch, nil
}

func (controller *AgentCheckpointRecoveryController) decide(ctx context.Context, source checkpoint.Attempt, manifest checkpoint.Manifest) (checkpoint.FallbackMode, error) {
	authority := controller.authority()
	if authority == nil {
		return checkpoint.FallbackManualReview, errors.New("no static recovery authority for admitted provenance")
	}
	if err := manifest.Validate(); err != nil {
		return checkpoint.FallbackManualReview, fmt.Errorf("invalid checkpoint source: %w", err)
	}
	if err := controller.matchesImmutable(manifest); err != nil {
		return checkpoint.FallbackManualReview, err
	}
	if _, err := controller.cleanJournal(ctx, source.ExecutionAttempt, authority); err != nil {
		return checkpoint.FallbackManualReview, err
	}
	if manifest.Archive == nil {
		return checkpoint.FallbackManualReview, errors.New("checkpoint source has no archive")
	}
	if !authority.Capabilities.SafeBoundary {
		return checkpoint.FallbackManualReview, errors.New("checkpoint archive lacks safe-boundary authority")
	}
	if manifest.ExecutionAttempt == source.ExecutionAttempt && manifest.SessionID != "" && authority.Capabilities.SessionExport && authority.Capabilities.NativeResume && authority.RecoveryProof != nil && authority.RecoveryProof.ValidateFor(authority.Adapter) == nil {
		return checkpoint.FallbackNativeResume, nil
	}
	return checkpoint.FallbackWorkspaceOnly, nil
}

func (controller *AgentCheckpointRecoveryController) validateFrozen(ctx context.Context, attempt checkpoint.Attempt, manifest checkpoint.Manifest) error {
	if err := manifest.Validate(); err != nil {
		return fmt.Errorf("invalid frozen checkpoint source: %w", err)
	}
	if err := controller.matchesImmutable(manifest); err != nil {
		return err
	}
	authority := controller.authority()
	if _, err := controller.cleanJournal(ctx, attempt.SourceExecutionAttempt, authority); err != nil {
		return err
	}
	if manifest.Archive == nil || !authority.Capabilities.SafeBoundary {
		return errors.New("frozen recovery source lacks archive safe-boundary authority")
	}
	switch attempt.Mode {
	case checkpoint.FallbackWorkspaceOnly:
		return nil
	case checkpoint.FallbackNativeResume:
		if manifest.ExecutionAttempt != attempt.SourceExecutionAttempt || manifest.SessionID == "" || !authority.Capabilities.SessionExport || !authority.Capabilities.NativeResume || authority.RecoveryProof == nil {
			return errors.New("frozen native recovery is no longer safe")
		}
		return authority.RecoveryProof.ValidateFor(authority.Adapter)
	default:
		return errors.New("frozen recovery mode is not automatic")
	}
}

func (controller *AgentCheckpointRecoveryController) matchesImmutable(manifest checkpoint.Manifest) error {
	p := controller.config.Provenance
	if !sameAgentCheckpointIdentity(manifest.Identity(), p.Identity) || manifest.Provider != p.Provider || manifest.RuntimeImage != p.RuntimeImage || manifest.Model != p.Model || manifest.ConfigDigest != p.ConfigDigest || manifest.InputDigest != p.InputDigest || manifest.MCPDigest != p.MCPDigest || manifest.SkillDigest != p.SkillDigest {
		return errors.New("checkpoint manifest immutable provenance differs from admitted execution")
	}
	return nil
}

func (controller *AgentCheckpointRecoveryController) authority() *AgentCheckpointRecoveryAuthority {
	p := controller.config.Provenance
	for index := range controller.config.Authorities {
		authority := &controller.config.Authorities[index]
		if authority.Provider == p.Provider && authority.Adapter == p.Adapter && authority.RuntimeImage == p.RuntimeImage {
			return authority
		}
	}
	return nil
}

func (controller *AgentCheckpointRecoveryController) cleanJournal(ctx context.Context, sourceAttempt int, authority *AgentCheckpointRecoveryAuthority) ([]checkpoint.Effect, error) {
	if authority == nil || !authority.CompleteEffectJournal || !authority.Capabilities.EffectJournal || authority.Capabilities.Version != authority.Adapter.Version {
		return nil, errors.New("complete server-owned effect journal authority is required")
	}
	effects, err := controller.journal.ListEffects(ctx, controller.config.Provenance.Identity, sourceAttempt)
	if err != nil {
		return nil, err
	}
	ambiguous := 0
	for _, effect := range effects {
		if err := effect.Validate(); err != nil ||
			effect.State != checkpoint.EffectCommitted ||
			!effect.ReadOnly ||
			effect.Provider != authority.Provider ||
			effect.AdapterVersion != authority.Adapter.Version {
			ambiguous++
		}
	}
	if ambiguous > 0 {
		if controller.config.Metrics != nil {
			controller.config.Metrics.RecordAmbiguousEffects(ctx, int64(ambiguous))
		}
		return nil, errors.New("interrupted effect journal is incomplete or unsafe")
	}
	return effects, nil
}

func (controller *AgentCheckpointRecoveryController) manualSource(ctx context.Context, source checkpoint.Attempt, cause error) (AgentCheckpointRecoveryLaunch, error) {
	manual, err := controller.attempts.MarkManualReview(ctx, checkpoint.MarkAttemptManualReviewRequest{Identity: controller.config.Provenance.Identity, ExecutionAttempt: source.ExecutionAttempt, ExpectedState: checkpoint.AttemptInterrupted})
	if err != nil {
		return AgentCheckpointRecoveryLaunch{}, errors.Join(cause, err)
	}
	return AgentCheckpointRecoveryLaunch{Attempt: manual, ManualReview: true}, nil
}

func (controller *AgentCheckpointRecoveryController) manualReplacement(ctx context.Context, attempt checkpoint.Attempt, cause error) (AgentCheckpointRecoveryLaunch, error) {
	if attempt.State == checkpoint.AttemptRunning {
		return AgentCheckpointRecoveryLaunch{}, cause
	}
	manual, err := controller.attempts.MarkManualReview(ctx, checkpoint.MarkAttemptManualReviewRequest{Identity: controller.config.Provenance.Identity, ExecutionAttempt: attempt.ExecutionAttempt, ExpectedState: attempt.State, MaterializationID: attempt.MaterializationID})
	if err != nil {
		return AgentCheckpointRecoveryLaunch{}, errors.Join(cause, err)
	}
	return AgentCheckpointRecoveryLaunch{Attempt: manual, ManualReview: true}, nil
}

// MarkMaterializationFailed terminalizes only the exact allocated replacement
// before process attachment; it cannot retroactively terminalize a source.
func (controller *AgentCheckpointRecoveryController) MarkMaterializationFailed(ctx context.Context, launch AgentCheckpointRecoveryLaunch) (checkpoint.Attempt, error) {
	expected := launch.Attempt
	current, found, err := controller.attempts.Current(ctx, controller.config.Provenance.Identity)
	if err != nil {
		return checkpoint.Attempt{}, err
	}
	if !found || current.ExecutionAttempt != expected.ExecutionAttempt ||
		current.MaterializationID != expected.MaterializationID ||
		current.SourceExecutionAttempt <= 0 {
		return checkpoint.Attempt{}, errors.New("materialization failure no longer identifies the exact recovery attempt")
	}
	if current.State != checkpoint.AttemptScheduling && current.State != checkpoint.AttemptMaterializing {
		return checkpoint.Attempt{}, errors.New("only a pre-launch recovery attempt can fail materialization")
	}
	return controller.attempts.MarkManualReview(ctx, checkpoint.MarkAttemptManualReviewRequest{
		Identity: controller.config.Provenance.Identity, ExecutionAttempt: current.ExecutionAttempt,
		ExpectedState: current.State, MaterializationID: current.MaterializationID,
	})
}

func recoveryMaterializationID(identity checkpoint.Identity, sourceAttempt int, checkpointID int64, generation int, mode checkpoint.FallbackMode) string {
	value := fmt.Sprintf("%d:%s:%s:%d:%d:%d:%s", identity.BuildID, identity.PlanID, identity.FunctionID, sourceAttempt, checkpointID, generation, mode)
	digest := sha256.Sum256([]byte(value))
	return "agent-checkpoint-recovery-" + hex.EncodeToString(digest[:])
}

func canonicalToolIDs(ids []string) []string {
	copyIDs := append([]string{}, ids...)
	sort.Strings(copyIDs)
	return copyIDs
}
