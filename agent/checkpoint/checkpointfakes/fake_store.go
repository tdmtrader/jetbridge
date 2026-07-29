package checkpointfakes

import (
	"context"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
)

// FakeStore is a concurrency-safe test double for the durable checkpoint
// authority. It records inputs at the recovery boundary rather than offering
// test-only methods on the production Store interface.
type FakeStore struct {
	mutex sync.Mutex

	BeginStub                        func(context.Context, checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error)
	AbortStub                        func(context.Context, checkpoint.AbortRequest) error
	PrepareObjectUploadStub          func(context.Context, checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error)
	CompleteObjectUploadStub         func(context.Context, checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error)
	CommitStub                       func(context.Context, checkpoint.CommitRequest) (checkpoint.Manifest, error)
	LatestStub                       func(context.Context, checkpoint.Identity) (checkpoint.Manifest, bool, error)
	MarkTerminalStub                 func(context.Context, checkpoint.Identity, time.Time) error
	ClaimCheckpointExpirationsStub   func(context.Context, int) ([]checkpoint.ExpirationClaim, error)
	FinalizeCheckpointExpirationStub func(context.Context, checkpoint.ExpirationClaim) error
	ClaimUnreferencedObjectsStub     func(context.Context, int) ([]checkpoint.ObjectDeleteClaim, error)
	FinalizeObjectDeletionStub       func(context.Context, checkpoint.ObjectDeleteClaim) error
	CleanupTerminalMetadataStub      func(context.Context, int) (int, error)

	beginCalls                        []BeginCall
	abortCalls                        []AbortCall
	prepareObjectUploadCalls          []PrepareObjectUploadCall
	completeObjectUploadCalls         []CompleteObjectUploadCall
	commitCalls                       []CommitCall
	latestCalls                       []LatestCall
	markTerminalCalls                 []MarkTerminalCall
	claimCheckpointExpirationsCalls   []ClaimCheckpointExpirationsCall
	finalizeCheckpointExpirationCalls []FinalizeCheckpointExpirationCall
	claimUnreferencedObjectsCalls     []ClaimUnreferencedObjectsCall
	finalizeObjectDeletionCalls       []FinalizeObjectDeletionCall
	cleanupTerminalMetadataCalls      []CleanupTerminalMetadataCall

	beginResult struct {
		stage checkpoint.StagedCheckpoint
		err   error
	}
	abortResult               error
	prepareObjectUploadResult struct {
		ticket checkpoint.ObjectUploadTicket
		err    error
	}
	completeObjectUploadResult struct {
		object hangar.ObjectRef
		err    error
	}
	commitResult struct {
		manifest checkpoint.Manifest
		err      error
	}
	latestResult struct {
		manifest checkpoint.Manifest
		found    bool
		err      error
	}
	markTerminalResult               error
	claimCheckpointExpirationsResult struct {
		claims []checkpoint.ExpirationClaim
		err    error
	}
	finalizeCheckpointExpirationResult error
	claimUnreferencedObjectsResult     struct {
		claims []checkpoint.ObjectDeleteClaim
		err    error
	}
	finalizeObjectDeletionResult  error
	cleanupTerminalMetadataResult struct {
		count int
		err   error
	}
}

var _ checkpoint.Store = (*FakeStore)(nil)

type BeginCall struct {
	Context context.Context
	Request checkpoint.BeginRequest
}

type AbortCall struct {
	Context context.Context
	Request checkpoint.AbortRequest
}

type PrepareObjectUploadCall struct {
	Context context.Context
	Request checkpoint.PrepareObjectUploadRequest
}

type CompleteObjectUploadCall struct {
	Context context.Context
	Request checkpoint.CompleteObjectUploadRequest
}

type CommitCall struct {
	Context context.Context
	Request checkpoint.CommitRequest
}

type LatestCall struct {
	Context  context.Context
	Identity checkpoint.Identity
}

type MarkTerminalCall struct {
	Context    context.Context
	Identity   checkpoint.Identity
	TerminalAt time.Time
}

type ClaimCheckpointExpirationsCall struct {
	Context context.Context
	Limit   int
}

type FinalizeCheckpointExpirationCall struct {
	Context context.Context
	Claim   checkpoint.ExpirationClaim
}

type ClaimUnreferencedObjectsCall struct {
	Context context.Context
	Limit   int
}

type FinalizeObjectDeletionCall struct {
	Context context.Context
	Claim   checkpoint.ObjectDeleteClaim
}

type CleanupTerminalMetadataCall struct {
	Context context.Context
	Limit   int
}

func (fake *FakeStore) Begin(ctx context.Context, request checkpoint.BeginRequest) (checkpoint.StagedCheckpoint, error) {
	fake.mutex.Lock()
	fake.beginCalls = append(fake.beginCalls, BeginCall{Context: ctx, Request: request.Clone()})
	stub, result := fake.BeginStub, fake.beginResult
	fake.mutex.Unlock()
	if stub != nil {
		stage, err := stub(ctx, request.Clone())
		return stage.Clone(), err
	}
	return result.stage.Clone(), result.err
}

func (fake *FakeStore) Abort(ctx context.Context, request checkpoint.AbortRequest) error {
	fake.mutex.Lock()
	fake.abortCalls = append(fake.abortCalls, AbortCall{Context: ctx, Request: request})
	stub, result := fake.AbortStub, fake.abortResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, request)
	}
	return result
}

func (fake *FakeStore) PrepareObjectUpload(ctx context.Context, request checkpoint.PrepareObjectUploadRequest) (checkpoint.ObjectUploadTicket, error) {
	fake.mutex.Lock()
	fake.prepareObjectUploadCalls = append(fake.prepareObjectUploadCalls, PrepareObjectUploadCall{Context: ctx, Request: request})
	stub, result := fake.PrepareObjectUploadStub, fake.prepareObjectUploadResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, request)
	}
	return result.ticket, result.err
}

func (fake *FakeStore) CompleteObjectUpload(ctx context.Context, request checkpoint.CompleteObjectUploadRequest) (hangar.ObjectRef, error) {
	fake.mutex.Lock()
	fake.completeObjectUploadCalls = append(fake.completeObjectUploadCalls, CompleteObjectUploadCall{Context: ctx, Request: request.Clone()})
	stub, result := fake.CompleteObjectUploadStub, fake.completeObjectUploadResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, request.Clone())
	}
	return result.object, result.err
}

func (fake *FakeStore) Commit(ctx context.Context, request checkpoint.CommitRequest) (checkpoint.Manifest, error) {
	fake.mutex.Lock()
	fake.commitCalls = append(fake.commitCalls, CommitCall{Context: ctx, Request: request.Clone()})
	stub, result := fake.CommitStub, fake.commitResult
	fake.mutex.Unlock()
	if stub != nil {
		manifest, err := stub(ctx, request.Clone())
		return manifest.Clone(), err
	}
	return result.manifest.Clone(), result.err
}

func (fake *FakeStore) Latest(ctx context.Context, identity checkpoint.Identity) (checkpoint.Manifest, bool, error) {
	fake.mutex.Lock()
	fake.latestCalls = append(fake.latestCalls, LatestCall{Context: ctx, Identity: identity.Clone()})
	stub, result := fake.LatestStub, fake.latestResult
	fake.mutex.Unlock()
	if stub != nil {
		manifest, found, err := stub(ctx, identity.Clone())
		return manifest.Clone(), found, err
	}
	return result.manifest.Clone(), result.found, result.err
}

func (fake *FakeStore) MarkTerminal(ctx context.Context, identity checkpoint.Identity, terminalAt time.Time) error {
	fake.mutex.Lock()
	fake.markTerminalCalls = append(fake.markTerminalCalls, MarkTerminalCall{Context: ctx, Identity: identity.Clone(), TerminalAt: terminalAt})
	stub, result := fake.MarkTerminalStub, fake.markTerminalResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, identity.Clone(), terminalAt)
	}
	return result
}

func (fake *FakeStore) ClaimCheckpointExpirations(ctx context.Context, limit int) ([]checkpoint.ExpirationClaim, error) {
	fake.mutex.Lock()
	fake.claimCheckpointExpirationsCalls = append(fake.claimCheckpointExpirationsCalls, ClaimCheckpointExpirationsCall{Context: ctx, Limit: limit})
	stub, result := fake.ClaimCheckpointExpirationsStub, fake.claimCheckpointExpirationsResult
	fake.mutex.Unlock()
	if stub != nil {
		claims, err := stub(ctx, limit)
		return cloneExpirationClaims(claims), err
	}
	return cloneExpirationClaims(result.claims), result.err
}

func (fake *FakeStore) FinalizeCheckpointExpiration(ctx context.Context, claim checkpoint.ExpirationClaim) error {
	fake.mutex.Lock()
	fake.finalizeCheckpointExpirationCalls = append(fake.finalizeCheckpointExpirationCalls, FinalizeCheckpointExpirationCall{Context: ctx, Claim: claim.Clone()})
	stub, result := fake.FinalizeCheckpointExpirationStub, fake.finalizeCheckpointExpirationResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, claim.Clone())
	}
	return result
}

func (fake *FakeStore) ClaimUnreferencedObjects(ctx context.Context, limit int) ([]checkpoint.ObjectDeleteClaim, error) {
	fake.mutex.Lock()
	fake.claimUnreferencedObjectsCalls = append(fake.claimUnreferencedObjectsCalls, ClaimUnreferencedObjectsCall{Context: ctx, Limit: limit})
	stub, result := fake.ClaimUnreferencedObjectsStub, fake.claimUnreferencedObjectsResult
	fake.mutex.Unlock()
	if stub != nil {
		claims, err := stub(ctx, limit)
		return cloneObjectDeleteClaims(claims), err
	}
	return cloneObjectDeleteClaims(result.claims), result.err
}

func (fake *FakeStore) FinalizeObjectDeletion(ctx context.Context, claim checkpoint.ObjectDeleteClaim) error {
	fake.mutex.Lock()
	fake.finalizeObjectDeletionCalls = append(fake.finalizeObjectDeletionCalls, FinalizeObjectDeletionCall{Context: ctx, Claim: claim.Clone()})
	stub, result := fake.FinalizeObjectDeletionStub, fake.finalizeObjectDeletionResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, claim.Clone())
	}
	return result
}

func (fake *FakeStore) CleanupTerminalMetadata(ctx context.Context, limit int) (int, error) {
	fake.mutex.Lock()
	fake.cleanupTerminalMetadataCalls = append(fake.cleanupTerminalMetadataCalls, CleanupTerminalMetadataCall{Context: ctx, Limit: limit})
	stub, result := fake.CleanupTerminalMetadataStub, fake.cleanupTerminalMetadataResult
	fake.mutex.Unlock()
	if stub != nil {
		return stub(ctx, limit)
	}
	return result.count, result.err
}

func (fake *FakeStore) BeginCalls() []BeginCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]BeginCall, len(fake.beginCalls))
	for index, call := range fake.beginCalls {
		calls[index] = BeginCall{Context: call.Context, Request: call.Request.Clone()}
	}
	return calls
}

func (fake *FakeStore) AbortCalls() []AbortCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]AbortCall(nil), fake.abortCalls...)
}

func (fake *FakeStore) PrepareObjectUploadCalls() []PrepareObjectUploadCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]PrepareObjectUploadCall(nil), fake.prepareObjectUploadCalls...)
}

func (fake *FakeStore) CompleteObjectUploadCalls() []CompleteObjectUploadCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]CompleteObjectUploadCall, len(fake.completeObjectUploadCalls))
	for index, call := range fake.completeObjectUploadCalls {
		calls[index] = CompleteObjectUploadCall{Context: call.Context, Request: call.Request.Clone()}
	}
	return calls
}

func (fake *FakeStore) CommitCalls() []CommitCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]CommitCall, len(fake.commitCalls))
	for index, call := range fake.commitCalls {
		calls[index] = CommitCall{Context: call.Context, Request: call.Request.Clone()}
	}
	return calls
}

func (fake *FakeStore) LatestCalls() []LatestCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]LatestCall, len(fake.latestCalls))
	for index, call := range fake.latestCalls {
		calls[index] = LatestCall{Context: call.Context, Identity: call.Identity.Clone()}
	}
	return calls
}

func (fake *FakeStore) MarkTerminalCalls() []MarkTerminalCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]MarkTerminalCall, len(fake.markTerminalCalls))
	for index, call := range fake.markTerminalCalls {
		calls[index] = MarkTerminalCall{Context: call.Context, Identity: call.Identity.Clone(), TerminalAt: call.TerminalAt}
	}
	return calls
}

func (fake *FakeStore) ClaimCheckpointExpirationsCalls() []ClaimCheckpointExpirationsCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]ClaimCheckpointExpirationsCall(nil), fake.claimCheckpointExpirationsCalls...)
}

func (fake *FakeStore) FinalizeCheckpointExpirationCalls() []FinalizeCheckpointExpirationCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]FinalizeCheckpointExpirationCall, len(fake.finalizeCheckpointExpirationCalls))
	for index, call := range fake.finalizeCheckpointExpirationCalls {
		calls[index] = FinalizeCheckpointExpirationCall{Context: call.Context, Claim: call.Claim.Clone()}
	}
	return calls
}

func (fake *FakeStore) ClaimUnreferencedObjectsCalls() []ClaimUnreferencedObjectsCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]ClaimUnreferencedObjectsCall(nil), fake.claimUnreferencedObjectsCalls...)
}

func (fake *FakeStore) FinalizeObjectDeletionCalls() []FinalizeObjectDeletionCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	calls := make([]FinalizeObjectDeletionCall, len(fake.finalizeObjectDeletionCalls))
	for index, call := range fake.finalizeObjectDeletionCalls {
		calls[index] = FinalizeObjectDeletionCall{Context: call.Context, Claim: call.Claim.Clone()}
	}
	return calls
}

func (fake *FakeStore) CleanupTerminalMetadataCalls() []CleanupTerminalMetadataCall {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]CleanupTerminalMetadataCall(nil), fake.cleanupTerminalMetadataCalls...)
}

func (fake *FakeStore) SetBeginResult(stage checkpoint.StagedCheckpoint, err error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.beginResult = struct {
		stage checkpoint.StagedCheckpoint
		err   error
	}{stage: stage.Clone(), err: err}
}

func cloneExpirationClaims(claims []checkpoint.ExpirationClaim) []checkpoint.ExpirationClaim {
	cloned := make([]checkpoint.ExpirationClaim, len(claims))
	for index, claim := range claims {
		cloned[index] = claim.Clone()
	}
	return cloned
}

func cloneObjectDeleteClaims(claims []checkpoint.ObjectDeleteClaim) []checkpoint.ObjectDeleteClaim {
	cloned := make([]checkpoint.ObjectDeleteClaim, len(claims))
	for index, claim := range claims {
		cloned[index] = claim.Clone()
	}
	return cloned
}
