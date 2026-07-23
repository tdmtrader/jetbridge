package resourcecapture

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

type CaptureOutputFinder interface {
	FindResourceCaptureOutput(context.Context, int, int64, string, string, snapshot.TypeRef) (snapshot.Snapshot, bool, error)
}

type SnapshotPinner interface {
	Pin(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef, string) (snapshot.RetentionClaim, error)
}

type DurableOutputStore struct {
	finder CaptureOutputFinder
	pinner SnapshotPinner
	locks  snapshot.DigestLockManager
}

func NewOutputStore(finder CaptureOutputFinder, pinner SnapshotPinner, locks snapshot.DigestLockManager) (*DurableOutputStore, error) {
	if isNil(finder) || isNil(pinner) || isNil(locks) {
		return nil, fmt.Errorf("resource capture: output finder, pinner, and digest lock manager are required")
	}
	return &DurableOutputStore{finder: finder, pinner: pinner, locks: locks}, nil
}

func (store *DurableOutputStore) Finalize(ctx context.Context, request OutputRequest) (snapshot.Snapshot, bool, error) {
	if err := contextError(ctx); err != nil {
		return snapshot.Snapshot{}, false, err
	}
	if request.TeamID <= 0 || request.TeamName == "" || request.PipelineRunID <= 0 || request.OutputPort == "" || request.Actor == "" {
		return snapshot.Snapshot{}, false, fmt.Errorf("resource capture: invalid output request")
	}
	if err := request.ExpectedType.Validate(); err != nil {
		return snapshot.Snapshot{}, false, err
	}
	decoded, err := hex.DecodeString(request.OperationKey)
	if err != nil || len(decoded) != 32 || request.OperationKey != strings.ToLower(request.OperationKey) {
		return snapshot.Snapshot{}, false, fmt.Errorf("resource capture: invalid operation key")
	}
	manifest, found, err := store.finder.FindResourceCaptureOutput(
		ctx, request.TeamID, request.PipelineRunID, request.OperationKey, request.OutputPort, request.ExpectedType,
	)
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	if !found {
		return snapshot.Snapshot{}, false, ErrOutputUnavailable
	}
	if err := manifest.Validate(); err != nil || manifest.Type != request.ExpectedType || manifest.ContentState != snapshot.ContentStateAvailable {
		return snapshot.Snapshot{}, false, ErrOutputUnavailable
	}
	ref := snapshot.SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest}
	err = snapshot.WithDigestLease(ctx, store.locks, []snapshot.Digest{manifest.Digest}, func(lease snapshot.DigestLease) error {
		_, err := store.pinner.Pin(ctx, lease, request.TeamID, request.Actor, ref, "resource capture "+request.OperationKey)
		return err
	})
	if err != nil {
		return snapshot.Snapshot{}, false, err
	}
	return manifest.Clone(), true, nil
}
