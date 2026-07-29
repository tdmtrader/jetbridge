package exec

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/checkpoint/checkpointfakes"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/atc/runtime"
)

func TestAgentCheckpointCaptureCommitsInExactOrderAndReleasesAllLeases(t *testing.T) {
	events := []string{}
	store := new(checkpointfakes.FakeStore)
	fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
	staged := checkpoint.StagedCheckpoint{ID: 17, HeadID: 5, Identity: captureIdentity(), Generation: 4, ExpectedPreviousGeneration: 3, ExecutionAttempt: 3, Fence: fence}
	object := captureObjectRef(9)
	prepared := checkpoint.PreparedArchive{Handle: "prepared-1", Digest: captureDigest(), Files: 2, Bytes: 256}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 19, StagedCheckpointID: staged.ID, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: object.Key, UploadToken: "upload-token"}
	store.BeginStub = func(_ context.Context, request checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error) {
		events = append(events, "begin")
		if request.Fence != fence || request.Identity != captureIdentity() {
			t.Fatalf("begin request = %#v", request)
		}
		return staged, nil
	}
	store.PrepareObjectUploadStub = func(_ context.Context, request checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error) {
		events = append(events, "prepare-upload")
		if request.StagedCheckpointID != staged.ID || request.Digest != prepared.Digest || request.Key != object.Key || request.Fence != fence {
			t.Fatalf("prepare upload request = %#v", request)
		}
		return ticket, nil
	}
	store.CompleteObjectUploadStub = func(_ context.Context, request checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error) {
		events = append(events, "complete-upload")
		if request.Ticket != ticket || request.Object != object || request.Fence != fence {
			t.Fatalf("complete upload request = %#v", request)
		}
		return object, nil
	}
	store.CommitStub = func(_ context.Context, request checkpoint.CommitRequest) (checkpoint.Manifest, error) {
		events = append(events, "commit")
		if request.StagedCheckpointID != staged.ID || request.ExpectedPreviousGeneration != 3 || request.Fence != fence || request.Manifest.Archive == nil || *request.Manifest.Archive != object || request.Manifest.SafeAt.IsZero() {
			t.Fatalf("commit request = %#v", request)
		}
		return request.Manifest, nil
	}

	attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence, ExpiresAt: time.Now().Add(time.Minute)}}
	boundary := &captureBoundary{events: &events}
	quiescence := &captureQuiescence{events: &events, target: captureTarget()}
	daemon := &captureDaemon{events: &events, prepared: prepared, object: object}

	coordinator := NewAgentCheckpointCapture(store, attempts, daemon)
	result, err := coordinator.Capture(context.Background(), AgentCheckpointCaptureRequest{
		Trigger:         CheckpointCaptureTriggerElapsed,
		Provenance:      captureProvenance(),
		FenceToken:      fence.Token,
		FenceTTL:        time.Minute,
		MaxArchiveBytes: 1024,
		Boundary:        boundary,
		Quiescence:      quiescence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != CheckpointCaptureCommitted || result.Manifest.CheckpointID != staged.ID || result.Manifest.Generation != staged.Generation {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"fence", "boundary", "quiesce", "begin", "prepare", "prepare-upload", "upload", "complete-upload", "commit", "quiesce-release", "boundary-release", "fence-release"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v\nwant = %v", events, want)
	}
}

func TestAgentCheckpointCaptureAbortsFailedStageAndKeepsHeadUncommitted(t *testing.T) {
	events := []string{}
	store := new(checkpointfakes.FakeStore)
	fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
	staged := checkpoint.StagedCheckpoint{ID: 17, Identity: captureIdentity(), Generation: 4, ExpectedPreviousGeneration: 3, ExecutionAttempt: 3, Fence: fence}
	store.BeginStub = func(context.Context, checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error) {
		events = append(events, "begin")
		return staged, nil
	}
	store.AbortStub = func(_ context.Context, request checkpoint.AbortRequest) error {
		events = append(events, "abort")
		if request.StagedCheckpointID != staged.ID || request.Fence != fence {
			t.Fatalf("abort request = %#v", request)
		}
		return nil
	}
	daemon := &captureDaemon{events: &events, prepareErr: errors.New("daemon unavailable")}
	attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence, ExpiresAt: time.Now().Add(time.Minute)}}
	boundary := &captureBoundary{events: &events}
	quiescence := &captureQuiescence{events: &events, target: captureTarget()}

	_, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), captureRequest(fence, boundary, quiescence))
	if !errors.Is(err, daemon.prepareErr) {
		t.Fatalf("capture error = %v", err)
	}
	if got := len(store.CommitCalls()); got != 0 {
		t.Fatalf("commit calls = %d, want 0", got)
	}
	want := []string{"fence", "boundary", "quiesce", "begin", "prepare", "abort", "quiesce-release", "boundary-release", "fence-release"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v\nwant = %v", events, want)
	}
}

func TestAgentCheckpointCaptureReconcilesAmbiguousCommitBeforeAbort(t *testing.T) {
	events := []string{}
	store := new(checkpointfakes.FakeStore)
	fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
	staged := checkpoint.StagedCheckpoint{ID: 17, Identity: captureIdentity(), Generation: 4, ExpectedPreviousGeneration: 3, ExecutionAttempt: 3, Fence: fence}
	object := captureObjectRef(9)
	prepared := checkpoint.PreparedArchive{Handle: "prepared-1", Digest: captureDigest(), Files: 2, Bytes: 256}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 19, StagedCheckpointID: staged.ID, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: object.Key, UploadToken: "upload-token"}
	store.BeginStub = func(context.Context, checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error) {
		events = append(events, "begin")
		return staged, nil
	}
	store.PrepareObjectUploadStub = func(context.Context, checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error) {
		events = append(events, "prepare-upload")
		return ticket, nil
	}
	store.CompleteObjectUploadStub = func(context.Context, checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error) {
		events = append(events, "complete-upload")
		return object, nil
	}
	store.CommitStub = func(_ context.Context, request checkpoint.CommitRequest) (checkpoint.Manifest, error) {
		events = append(events, "commit")
		return checkpoint.Manifest{}, errors.New("timeout")
	}
	store.LatestStub = func(_ context.Context, _ checkpoint.Identity) (checkpoint.Manifest, bool, error) {
		events = append(events, "latest")
		return checkpoint.Manifest{CheckpointID: staged.ID, Generation: staged.Generation, Archive: &object}, true, nil
	}
	store.AbortStub = func(context.Context, checkpoint.AbortRequest) error {
		t.Fatal("ambiguous committed stage was aborted")
		return nil
	}
	attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence, ExpiresAt: time.Now().Add(time.Minute)}}
	boundary := &captureBoundary{events: &events}
	quiescence := &captureQuiescence{events: &events, target: captureTarget()}
	daemon := &captureDaemon{events: &events, prepared: prepared, object: object}

	result, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), captureRequest(fence, boundary, quiescence))
	if err != nil || result.Status != CheckpointCaptureCommitted || result.Manifest.CheckpointID != staged.ID {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	want := []string{"fence", "boundary", "quiesce", "begin", "prepare", "prepare-upload", "upload", "complete-upload", "commit", "latest", "quiesce-release", "boundary-release", "fence-release"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v\nwant = %v", events, want)
	}
}

func TestAgentCheckpointCaptureCompletionSkipsBoundaryAndRejectsUnknownTriggerBeforeFence(t *testing.T) {
	t.Run("completion", func(t *testing.T) {
		events := []string{}
		fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
		store, daemon := captureStoreAndDaemon(t, &events, fence)
		attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence, ExpiresAt: time.Now().Add(time.Minute)}}
		request := captureRequest(fence, &captureBoundary{events: &events}, &captureQuiescence{events: &events, target: captureTarget()})
		request.Trigger = CheckpointCaptureTriggerCompletion
		if _, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event == "boundary" || event == "boundary-release" || event == "quiesce" {
				t.Fatalf("completion used a live capture authority: %v", events)
			}
		}
		if !containsCaptureEvent(events, "terminal-quiesce") {
			t.Fatalf("completion did not use terminal quiescence: %v", events)
		}
	})
	t.Run("unknown trigger", func(t *testing.T) {
		events := []string{}
		fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
		store, daemon := captureStoreAndDaemon(t, &events, fence)
		attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence, ExpiresAt: time.Now().Add(time.Minute)}}
		request := captureRequest(fence, nil, nil)
		request.Trigger = "provider-controlled"
		if _, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), request); err == nil {
			t.Fatal("accepted unclosed trigger")
		}
		if len(events) != 0 {
			t.Fatalf("invalid trigger acquired authority: %v", events)
		}
	})
}

func TestAgentCheckpointCaptureRecordsBoundedPhaseOutcomeAndLostWorkMetrics(t *testing.T) {
	events := []string{}
	fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
	store, daemon := captureStoreAndDaemon(t, &events, fence)
	attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence, ExpiresAt: time.Now().Add(time.Minute)}}
	metrics := &captureMetrics{}
	request := captureRequest(fence, &captureBoundary{events: &events}, &captureQuiescence{events: &events, target: captureTarget()})
	request.Provenance.PreviousSafeAt = time.Now().Add(-time.Minute)
	if _, err := NewAgentCheckpointCapture(store, attempts, daemon, WithAgentCheckpointCaptureMetrics(metrics)).Capture(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !metrics.hasDuration("requested_to_quiesced", CheckpointCaptureTriggerElapsed) || !metrics.hasDuration("archive", CheckpointCaptureTriggerElapsed) || !metrics.hasDuration("upload", CheckpointCaptureTriggerElapsed) || !metrics.hasDuration("total", CheckpointCaptureTriggerElapsed) {
		t.Fatalf("duration metrics = %#v", metrics.metrics)
	}
	if !metrics.hasOutcome("committed", CheckpointCaptureTriggerElapsed) || !metrics.hasOutcome("lost_work", CheckpointCaptureTriggerElapsed) {
		t.Fatalf("outcome metrics = %#v", metrics.metrics)
	}
}

func TestAgentCheckpointCaptureRejectsInvalidFenceBeforeAuthorityAndMismatchedTargetBeforeStage(t *testing.T) {
	t.Run("invalid fence", func(t *testing.T) {
		events := []string{}
		fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
		store, daemon := captureStoreAndDaemon(t, &events, fence)
		attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence}}
		request := captureRequest(fence, nil, nil)
		request.FenceToken = "not-a-uuid"
		if _, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), request); err == nil {
			t.Fatal("accepted invalid caller fence token")
		}
		if len(events) != 0 {
			t.Fatalf("invalid fence acquired authority: %v", events)
		}
	})
	t.Run("mismatched target", func(t *testing.T) {
		events := []string{}
		fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
		store, daemon := captureStoreAndDaemon(t, &events, fence)
		attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence}}
		target := captureTarget()
		target.ContainerHandle = "different-agent"
		request := captureRequest(fence, &captureBoundary{events: &events}, &captureQuiescence{events: &events, target: target})
		if _, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), request); err == nil {
			t.Fatal("accepted capture target outside caller-owned container")
		}
		if got := len(store.BeginCalls()); got != 0 {
			t.Fatalf("mismatched target started stage: %d", got)
		}
		want := []string{"fence", "boundary", "quiesce", "quiesce-release", "boundary-release", "fence-release"}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("events = %v\nwant = %v", events, want)
		}
	})
}

func TestAgentCheckpointCaptureSetsSafeAtOnlyAfterBoundaryAndQuiescence(t *testing.T) {
	events := []string{}
	fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
	store, daemon := captureStoreAndDaemon(t, &events, fence)
	attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence}}
	coordinator := NewAgentCheckpointCapture(store, attempts, daemon)
	coordinator.now = func() time.Time {
		if !reflect.DeepEqual(events, []string{"fence", "boundary", "quiesce"}) {
			t.Fatalf("SafeAt clock called before quiescence: %v", events)
		}
		return time.Unix(100, 0)
	}
	result, err := coordinator.Capture(context.Background(), captureRequest(fence, &captureBoundary{events: &events}, &captureQuiescence{events: &events, target: captureTarget()}))
	if err != nil || !result.Manifest.SafeAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}

func TestAgentCheckpointCaptureAbortsWhenSuccessfulCommitReturnsWrongHead(t *testing.T) {
	events := []string{}
	fence := checkpoint.FenceClaim{ExecutionAttempt: 3, Token: "11111111-1111-1111-1111-111111111111"}
	store, daemon := captureStoreAndDaemon(t, &events, fence)
	store.CommitStub = func(_ context.Context, request checkpoint.CommitRequest) (checkpoint.Manifest, error) {
		events = append(events, "commit")
		wrong := request.Manifest.Clone()
		wrong.Generation++
		return wrong, nil
	}
	store.AbortStub = func(context.Context, checkpoint.AbortRequest) error { events = append(events, "abort"); return nil }
	attempts := &captureAttemptStore{events: &events, fence: checkpoint.AttemptFence{FenceClaim: fence}}
	_, err := NewAgentCheckpointCapture(store, attempts, daemon).Capture(context.Background(), captureRequest(fence, &captureBoundary{events: &events}, &captureQuiescence{events: &events, target: captureTarget()}))
	if err == nil {
		t.Fatal("accepted mismatched committed head")
	}
	if got := len(store.AbortCalls()); got != 1 {
		t.Fatalf("abort calls = %d, want 1", got)
	}
}

func captureIdentity() checkpoint.Identity {
	return checkpoint.Identity{BuildID: 42, PlanID: "plan", FunctionID: "agent"}
}

func captureDigest() hangar.Digest {
	return hangar.Digest("sha256:2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824")
}

func captureObjectRef(generation int64) hangar.ObjectRef {
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, captureDigest(), generation)
	if err != nil {
		panic(err)
	}
	return ref
}

func captureTarget() runtime.CheckpointCaptureTarget {
	return runtime.CheckpointCaptureTarget{ContainerHandle: "agent-42", PodName: "pod-42", PodUID: "uid-42", NodeName: "node-a", Archive: checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{"session"}, MaxBytes: 1024}}
}

func captureProvenance() CheckpointCaptureProvenance {
	return CheckpointCaptureProvenance{
		Identity: captureIdentity(), ExecutionAttempt: 3,
		ContainerHandle: "agent-42",
		Provider:        "anthropic", RuntimeImage: "sha256:image", Model: "model",
		ConfigDigest: string(captureDigest()), InputDigest: string(captureDigest()), MCPDigest: string(captureDigest()), SkillDigest: string(captureDigest()),
		SessionID: "session-1", TranscriptCursor: 4,
	}
}

func captureRequest(fence checkpoint.FenceClaim, boundary runtime.SafeBoundaryProcess, quiescence runtime.CheckpointProcess) AgentCheckpointCaptureRequest {
	terminal, _ := quiescence.(runtime.TerminalCheckpointProcess)
	return AgentCheckpointCaptureRequest{Trigger: CheckpointCaptureTriggerElapsed, Provenance: captureProvenance(), FenceToken: fence.Token, FenceTTL: time.Minute, MaxArchiveBytes: 1024, Boundary: boundary, Quiescence: quiescence, TerminalQuiescence: terminal}
}

func captureStoreAndDaemon(t *testing.T, events *[]string, fence checkpoint.FenceClaim) (*checkpointfakes.FakeStore, *captureDaemon) {
	t.Helper()
	store := new(checkpointfakes.FakeStore)
	staged := checkpoint.StagedCheckpoint{ID: 17, Identity: captureIdentity(), Generation: 4, ExpectedPreviousGeneration: 3, ExecutionAttempt: 3, Fence: fence}
	object := captureObjectRef(9)
	prepared := checkpoint.PreparedArchive{Handle: "prepared-1", Digest: captureDigest(), Files: 2, Bytes: 256}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 19, StagedCheckpointID: staged.ID, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: object.Key, UploadToken: "upload-token"}
	store.BeginStub = func(context.Context, checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error) {
		*events = append(*events, "begin")
		return staged, nil
	}
	store.PrepareObjectUploadStub = func(context.Context, checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error) {
		*events = append(*events, "prepare-upload")
		return ticket, nil
	}
	store.CompleteObjectUploadStub = func(context.Context, checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error) {
		*events = append(*events, "complete-upload")
		return object, nil
	}
	store.CommitStub = func(_ context.Context, request checkpoint.CommitRequest) (checkpoint.Manifest, error) {
		*events = append(*events, "commit")
		return request.Manifest, nil
	}
	return store, &captureDaemon{events: events, prepared: prepared, object: object}
}

type captureAttemptStore struct {
	events *[]string
	fence  checkpoint.AttemptFence
}

func (store *captureAttemptStore) AcquireFence(_ context.Context, request checkpoint.AcquireAttemptFenceRequest) (checkpoint.AttemptFence, error) {
	*store.events = append(*store.events, "fence")
	return store.fence, nil
}

func (store *captureAttemptStore) ReleaseFence(_ context.Context, request checkpoint.ReleaseAttemptFenceRequest) error {
	*store.events = append(*store.events, "fence-release")
	return nil
}

type captureBoundary struct{ events *[]string }

func (boundary *captureBoundary) AcquireSafeBoundary(context.Context) (runtime.SafeBoundaryLease, error) {
	*boundary.events = append(*boundary.events, "boundary")
	return captureRelease{events: boundary.events, event: "boundary-release"}, nil
}

type captureQuiescence struct {
	events *[]string
	target runtime.CheckpointCaptureTarget
}

func (quiescence *captureQuiescence) AcquireCheckpointCapture(_ context.Context, maxBytes int64) (runtime.CheckpointCaptureLease, error) {
	*quiescence.events = append(*quiescence.events, "quiesce")
	if maxBytes != quiescence.target.Archive.MaxBytes {
		return nil, errors.New("wrong capture max bytes")
	}
	return captureLease{captureRelease: captureRelease{events: quiescence.events, event: "quiesce-release"}, target: quiescence.target}, nil
}

func (quiescence *captureQuiescence) AcquireTerminalCheckpointCapture(ctx context.Context, maxBytes int64) (runtime.CheckpointCaptureLease, error) {
	*quiescence.events = append(*quiescence.events, "terminal-quiesce")
	if maxBytes != quiescence.target.Archive.MaxBytes {
		return nil, errors.New("wrong terminal capture max bytes")
	}
	return captureLease{captureRelease: captureRelease{events: quiescence.events, event: "terminal-quiesce-release"}, target: quiescence.target}, nil
}

func containsCaptureEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

type captureRelease struct {
	events *[]string
	event  string
}

func (release captureRelease) Release(context.Context) error {
	*release.events = append(*release.events, release.event)
	return nil
}

type captureLease struct {
	captureRelease
	target runtime.CheckpointCaptureTarget
}

func (lease captureLease) CaptureTarget() runtime.CheckpointCaptureTarget { return lease.target }

type captureDaemon struct {
	events     *[]string
	prepared   checkpoint.PreparedArchive
	object     hangar.ObjectRef
	prepareErr error
}

type captureMetrics struct{ metrics []CheckpointCaptureMetric }

func (metrics *captureMetrics) RecordCheckpointCapture(metric CheckpointCaptureMetric) {
	metrics.metrics = append(metrics.metrics, metric)
}

func (metrics *captureMetrics) hasDuration(phase string, trigger CheckpointCaptureTrigger) bool {
	for _, metric := range metrics.metrics {
		if metric.Kind == CheckpointCaptureMetricDuration && metric.Phase == phase && metric.Trigger == trigger && metric.Duration >= 0 {
			return true
		}
	}
	return false
}

func (metrics *captureMetrics) hasOutcome(outcome string, trigger CheckpointCaptureTrigger) bool {
	for _, metric := range metrics.metrics {
		if metric.Kind == CheckpointCaptureMetricOutcome && metric.Outcome == outcome && metric.Trigger == trigger {
			return true
		}
	}
	return false
}

func (daemon *captureDaemon) PrepareCheckpoint(_ context.Context, node string, request checkpoint.ArchiveRequest) (checkpoint.PreparedArchive, error) {
	*daemon.events = append(*daemon.events, "prepare")
	if daemon.prepareErr != nil {
		return checkpoint.PreparedArchive{}, daemon.prepareErr
	}
	if node != "node-a" || !reflect.DeepEqual(request, captureTarget().Archive) {
		return checkpoint.PreparedArchive{}, errors.New("wrong exact-node prepare")
	}
	return daemon.prepared, nil
}

func (daemon *captureDaemon) UploadCheckpoint(_ context.Context, node string, prepared checkpoint.PreparedArchive, ticket checkpoint.ObjectUploadTicket) (checkpoint.ArchiveResult, error) {
	*daemon.events = append(*daemon.events, "upload")
	if node != "node-a" || prepared != daemon.prepared || ticket.Digest != prepared.Digest {
		return checkpoint.ArchiveResult{}, errors.New("wrong exact-node upload")
	}
	return checkpoint.ArchiveResult{Object: hangar.Attributes{Ref: daemon.object, UncompressedBytes: prepared.Bytes, CompressedBytes: 1}, Files: prepared.Files, Bytes: prepared.Bytes}, nil
}
