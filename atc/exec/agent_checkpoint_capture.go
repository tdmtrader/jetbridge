package exec

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/google/uuid"
)

// CheckpointCaptureTrigger is deliberately closed: these are server-owned
// capture causes, never provider-supplied labels.
type CheckpointCaptureTrigger string

const (
	CheckpointCaptureTriggerElapsed    CheckpointCaptureTrigger = "elapsed"
	CheckpointCaptureTriggerCompletion CheckpointCaptureTrigger = "completion"
	CheckpointCaptureTriggerExplicit   CheckpointCaptureTrigger = "explicit"
	CheckpointCaptureTriggerPreemption CheckpointCaptureTrigger = "preemption"
)

type CheckpointCaptureStatus string

const (
	CheckpointCaptureCommitted CheckpointCaptureStatus = "committed"
	CheckpointCaptureSkipped   CheckpointCaptureStatus = "skipped"
)

// CheckpointCaptureProvenance is pinned server-side execution evidence. The
// coordinator copies it into the committed manifest only after the exact
// runtime target has been independently verified.
type CheckpointCaptureProvenance struct {
	Identity             checkpoint.Identity
	ExecutionAttempt     int
	ContainerHandle      string
	Provider             string
	RuntimeImage         string
	Model                string
	ConfigDigest         string
	InputDigest          string
	MCPDigest            string
	SkillDigest          string
	SessionID            string
	TranscriptCursor     int64
	CompletedToolCallIDs []string
	Effects              []checkpoint.Effect
	PreviousSafeAt       time.Time
}

type AgentCheckpointCaptureRequest struct {
	Trigger            CheckpointCaptureTrigger
	Provenance         CheckpointCaptureProvenance
	FenceToken         string
	FenceTTL           time.Duration
	MaxArchiveBytes    int64
	Boundary           runtime.SafeBoundaryProcess
	Quiescence         runtime.CheckpointProcess
	TerminalQuiescence runtime.TerminalCheckpointProcess
}

type AgentCheckpointCaptureResult struct {
	Status   CheckpointCaptureStatus
	Manifest checkpoint.Manifest
}

// Narrow authority interfaces keep the pure coordinator independent from DB
// factories and Jetbridge's concrete daemon client.
type checkpointCaptureStore interface {
	Begin(context.Context, checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error)
	Abort(context.Context, checkpoint.AbortRequest) error
	PrepareObjectUpload(context.Context, checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error)
	CompleteObjectUpload(context.Context, checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error)
	Commit(context.Context, checkpoint.CommitRequest) (checkpoint.Manifest, error)
	Latest(context.Context, checkpoint.Identity) (checkpoint.Manifest, bool, error)
}

type checkpointAttemptFenceStore interface {
	AcquireFence(context.Context, checkpoint.AcquireAttemptFenceRequest) (checkpoint.AttemptFence, error)
	ReleaseFence(context.Context, checkpoint.ReleaseAttemptFenceRequest) error
}

type checkpointCaptureDaemon interface {
	PrepareCheckpoint(context.Context, string, checkpoint.ArchiveRequest) (checkpoint.PreparedArchive, error)
	UploadCheckpoint(context.Context, string, checkpoint.PreparedArchive, checkpoint.ObjectUploadTicket) (checkpoint.ArchiveResult, error)
}

type AgentCheckpointCapture struct {
	store    checkpointCaptureStore
	attempts checkpointAttemptFenceStore
	daemon   checkpointCaptureDaemon
	now      func() time.Time
	metrics  CheckpointCaptureMetrics
}

const checkpointCaptureCleanupTimeout = 5 * time.Second

func NewAgentCheckpointCapture(store checkpointCaptureStore, attempts checkpointAttemptFenceStore, daemon checkpointCaptureDaemon, options ...agentCheckpointCaptureOption) *AgentCheckpointCapture {
	coordinator := &AgentCheckpointCapture{store: store, attempts: attempts, daemon: daemon, now: time.Now}
	for _, option := range options {
		if option != nil {
			option(coordinator)
		}
	}
	return coordinator
}

func (coordinator *AgentCheckpointCapture) Capture(ctx context.Context, request AgentCheckpointCaptureRequest) (result AgentCheckpointCaptureResult, err error) {
	if err := validateCheckpointCaptureRequest(coordinator, ctx, request); err != nil {
		return result, err
	}
	requestedAt := time.Now()
	defer func() {
		coordinator.recordDuration(ctx, "total", request.Trigger, time.Since(requestedAt))
		switch {
		case err != nil:
			coordinator.recordOutcome(ctx, "failed", request.Trigger, 0)
		case result.Status == CheckpointCaptureSkipped:
			coordinator.recordOutcome(ctx, "skipped", request.Trigger, 0)
		case result.Status == CheckpointCaptureCommitted:
			coordinator.recordOutcome(ctx, "committed", request.Trigger, 0)
			if !request.Provenance.PreviousSafeAt.IsZero() && result.Manifest.SafeAt.After(request.Provenance.PreviousSafeAt) {
				coordinator.recordOutcome(ctx, "lost_work", request.Trigger, result.Manifest.SafeAt.Sub(request.Provenance.PreviousSafeAt))
			}
		}
	}()
	fence, err := coordinator.attempts.AcquireFence(ctx, checkpoint.AcquireAttemptFenceRequest{
		Identity: request.Provenance.Identity, ExecutionAttempt: request.Provenance.ExecutionAttempt,
		Token: request.FenceToken, TTL: request.FenceTTL,
	})
	if err != nil {
		return result, err
	}
	if fence.FenceClaim.ExecutionAttempt != request.Provenance.ExecutionAttempt || fence.FenceClaim.Token != request.FenceToken {
		_ = coordinator.releaseFence(request.Provenance.Identity, request.Provenance.ExecutionAttempt, request.FenceToken)
		return result, errors.New("checkpoint capture acquired mismatched attempt fence")
	}
	defer func() {
		releaseErr := coordinator.releaseFence(request.Provenance.Identity, request.Provenance.ExecutionAttempt, request.FenceToken)
		err = errors.Join(err, releaseErr)
	}()

	var quiescence runtime.CheckpointCaptureLease
	if request.Trigger != CheckpointCaptureTriggerCompletion {
		if request.Boundary == nil {
			return AgentCheckpointCaptureResult{Status: CheckpointCaptureSkipped}, nil
		}
		boundary, boundaryErr := request.Boundary.AcquireSafeBoundary(ctx)
		if boundaryErr != nil {
			return result, boundaryErr
		}
		if boundary == nil {
			return result, errors.New("checkpoint capture acquired nil safe boundary lease")
		}
		defer func() { err = errors.Join(err, releaseSafeBoundary(boundary)) }()
		if request.Quiescence == nil {
			return AgentCheckpointCaptureResult{Status: CheckpointCaptureSkipped}, nil
		}
		var quiescenceErr error
		quiescence, quiescenceErr = request.Quiescence.AcquireCheckpointCapture(ctx, request.MaxArchiveBytes)
		if quiescenceErr != nil {
			return result, quiescenceErr
		}
	} else {
		if request.TerminalQuiescence == nil {
			return AgentCheckpointCaptureResult{Status: CheckpointCaptureSkipped}, nil
		}
		var quiescenceErr error
		quiescence, quiescenceErr = request.TerminalQuiescence.AcquireTerminalCheckpointCapture(ctx, request.MaxArchiveBytes)
		if quiescenceErr != nil {
			return result, quiescenceErr
		}
	}
	if quiescence == nil {
		return result, errors.New("checkpoint capture acquired nil quiescence lease")
	}
	defer func() { err = errors.Join(err, releaseCheckpointCapture(quiescence)) }()
	coordinator.recordDuration(ctx, "requested_to_quiesced", request.Trigger, time.Since(requestedAt))
	target := quiescence.CaptureTarget()
	if err := target.Validate(); err != nil {
		return result, err
	}
	if target.ContainerHandle != request.Provenance.ContainerHandle || target.Archive.MaxBytes != request.MaxArchiveBytes {
		return result, errors.New("checkpoint capture target does not match pinned provenance")
	}

	safeAt := coordinator.now().UTC()
	staged, stageErr := coordinator.store.Begin(ctx, checkpoint.BeginRequest{Identity: request.Provenance.Identity, Fence: fence.FenceClaim, ExecutionAttempt: request.Provenance.ExecutionAttempt})
	if stageErr != nil {
		return result, stageErr
	}
	stagedOpen := true
	defer func() {
		if stagedOpen {
			abortCtx, cancel := checkpointCaptureCleanupContext()
			err = errors.Join(err, coordinator.store.Abort(abortCtx, checkpoint.AbortRequest{StagedCheckpointID: staged.ID, Fence: fence.FenceClaim}))
			cancel()
		}
	}()
	if !reflect.DeepEqual(staged.Identity, request.Provenance.Identity) || staged.ExecutionAttempt != request.Provenance.ExecutionAttempt || staged.Fence != fence.FenceClaim || staged.ID <= 0 || staged.Generation <= 0 || staged.ExpectedPreviousGeneration < 0 {
		return result, errors.New("checkpoint capture store returned invalid staged checkpoint")
	}

	archiveStarted := time.Now()
	prepared, prepareErr := coordinator.daemon.PrepareCheckpoint(ctx, target.NodeName, target.Archive)
	coordinator.recordDuration(ctx, "archive", request.Trigger, time.Since(archiveStarted))
	if prepareErr != nil {
		return result, prepareErr
	}
	if err := prepared.Validate(); err != nil {
		return result, err
	}
	ticket, ticketErr := coordinator.store.PrepareObjectUpload(ctx, checkpoint.PrepareObjectUploadRequest{StagedCheckpointID: staged.ID, Digest: prepared.Digest, Key: objectKey(prepared.Digest), Fence: fence.FenceClaim})
	if ticketErr != nil {
		return result, ticketErr
	}
	uploadStarted := time.Now()
	uploaded, uploadErr := coordinator.daemon.UploadCheckpoint(ctx, target.NodeName, prepared, ticket)
	coordinator.recordDuration(ctx, "upload", request.Trigger, time.Since(uploadStarted))
	if uploadErr != nil {
		return result, uploadErr
	}
	if err := validateCoordinatorArchive(uploaded, prepared, ticket); err != nil {
		return result, err
	}
	object, completeErr := coordinator.store.CompleteObjectUpload(ctx, checkpoint.CompleteObjectUploadRequest{Ticket: ticket, Object: uploaded.Object.Ref, Fence: fence.FenceClaim})
	if completeErr != nil {
		return result, completeErr
	}
	if object != uploaded.Object.Ref {
		return result, errors.New("checkpoint capture completed object differs from daemon upload")
	}
	manifest := checkpoint.Manifest{
		Version: 1, CheckpointID: staged.ID, Generation: staged.Generation, ExecutionAttempt: request.Provenance.ExecutionAttempt,
		WorkflowRunID: request.Provenance.Identity.Clone().WorkflowRunID, BuildID: request.Provenance.Identity.BuildID, PlanID: request.Provenance.Identity.PlanID, FunctionID: request.Provenance.Identity.FunctionID,
		Provider: request.Provenance.Provider, RuntimeImage: request.Provenance.RuntimeImage, Model: request.Provenance.Model,
		ConfigDigest: request.Provenance.ConfigDigest, InputDigest: request.Provenance.InputDigest, MCPDigest: request.Provenance.MCPDigest, SkillDigest: request.Provenance.SkillDigest,
		Archive: &object, SessionID: request.Provenance.SessionID, TranscriptCursor: request.Provenance.TranscriptCursor,
		CompletedToolCallIDs: append([]string(nil), request.Provenance.CompletedToolCallIDs...), Effects: append([]checkpoint.Effect(nil), request.Provenance.Effects...), SafeAt: safeAt,
	}.Canonicalized()
	if err := manifest.Validate(); err != nil {
		return result, err
	}
	committed, commitErr := coordinator.store.Commit(ctx, checkpoint.CommitRequest{StagedCheckpointID: staged.ID, ExpectedPreviousGeneration: staged.ExpectedPreviousGeneration, Manifest: manifest, Fence: fence.FenceClaim})
	if commitErr != nil {
		latest, found, latestErr := coordinator.store.Latest(ctx, request.Provenance.Identity)
		if latestErr == nil && found && latest.CheckpointID == staged.ID && latest.Generation == staged.Generation && latest.Archive != nil && *latest.Archive == object {
			stagedOpen = false
			return AgentCheckpointCaptureResult{Status: CheckpointCaptureCommitted, Manifest: latest.Clone()}, nil
		}
		return result, errors.Join(commitErr, latestErr)
	}
	if committed.CheckpointID != staged.ID || committed.Generation != staged.Generation || committed.ExecutionAttempt != request.Provenance.ExecutionAttempt || committed.Archive == nil || *committed.Archive != object {
		return result, errors.New("checkpoint capture commit returned a mismatched head")
	}
	stagedOpen = false
	return AgentCheckpointCaptureResult{Status: CheckpointCaptureCommitted, Manifest: committed.Clone()}, nil
}

func validateCheckpointCaptureRequest(coordinator *AgentCheckpointCapture, ctx context.Context, request AgentCheckpointCaptureRequest) error {
	if coordinator == nil || coordinator.store == nil || coordinator.attempts == nil || coordinator.daemon == nil || coordinator.now == nil || ctx == nil {
		return errors.New("checkpoint capture coordinator is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	switch request.Trigger {
	case CheckpointCaptureTriggerElapsed, CheckpointCaptureTriggerCompletion, CheckpointCaptureTriggerExplicit, CheckpointCaptureTriggerPreemption:
	default:
		return fmt.Errorf("checkpoint capture trigger %q is invalid", request.Trigger)
	}
	if err := request.Provenance.Identity.Validate(); err != nil || request.Provenance.ExecutionAttempt <= 0 || request.FenceTTL <= 0 || request.MaxArchiveBytes <= 0 || !validCheckpointFenceToken(request.FenceToken) {
		return errors.New("checkpoint capture request has invalid server provenance or fence")
	}
	for _, value := range []string{request.Provenance.ContainerHandle, request.Provenance.Provider, request.Provenance.RuntimeImage, request.Provenance.Model, request.Provenance.ConfigDigest, request.Provenance.InputDigest, request.Provenance.MCPDigest, request.Provenance.SkillDigest} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return errors.New("checkpoint capture provenance is incomplete or unpinned")
		}
	}
	if request.Provenance.TranscriptCursor < 0 {
		return errors.New("checkpoint capture transcript cursor is invalid")
	}
	for _, digest := range []string{request.Provenance.ConfigDigest, request.Provenance.InputDigest, request.Provenance.MCPDigest, request.Provenance.SkillDigest} {
		if err := hangar.Digest(digest).Validate(); err != nil {
			return err
		}
	}
	return nil
}

func validCheckpointFenceToken(token string) bool {
	parsed, err := uuid.Parse(token)
	return err == nil && parsed.String() == token
}

func checkpointCaptureCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), checkpointCaptureCleanupTimeout)
}

func (coordinator *AgentCheckpointCapture) releaseFence(identity checkpoint.Identity, attempt int, token string) error {
	ctx, cancel := checkpointCaptureCleanupContext()
	defer cancel()
	return coordinator.attempts.ReleaseFence(ctx, checkpoint.ReleaseAttemptFenceRequest{Identity: identity, ExecutionAttempt: attempt, Token: token})
}

func releaseSafeBoundary(lease runtime.SafeBoundaryLease) error {
	ctx, cancel := checkpointCaptureCleanupContext()
	defer cancel()
	return lease.Release(ctx)
}

func releaseCheckpointCapture(lease runtime.CheckpointCaptureLease) error {
	ctx, cancel := checkpointCaptureCleanupContext()
	defer cancel()
	return lease.Release(ctx)
}

func objectKey(digest hangar.Digest) string {
	key, _ := hangar.Key(hangar.KindCheckpoint, digest)
	return key
}

func validateCoordinatorArchive(result checkpoint.ArchiveResult, prepared checkpoint.PreparedArchive, ticket checkpoint.ObjectUploadTicket) error {
	if result.Object.Ref.Kind != hangar.KindCheckpoint || result.Object.Ref.Digest != ticket.Digest || result.Object.Ref.Key != ticket.Key || result.Object.Ref.Generation <= 0 || result.Files != prepared.Files || result.Bytes != prepared.Bytes {
		return errors.New("checkpoint capture daemon result does not match prepared archive")
	}
	return nil
}
