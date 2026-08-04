package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/concourse/concourse/agent/hangar"
)

func TestReclaimDeletesAnUnreferencedObjectPinnedToItsClaimedGeneration(t *testing.T) {
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{deletableClaim(t, 7, 41)}}}
	objects := &durableObjectStub{}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	if len(objects.deleted) != 1 {
		t.Fatalf("expected one durable delete, got %d", len(objects.deleted))
	}
	if got := objects.deleted[0]; got != deletableClaim(t, 7, 41).Object {
		t.Fatalf("delete was not pinned to the claimed object: %+v", got)
	}
	if len(store.finalizedObjects) != 1 || store.finalizedObjects[0].ObjectID != 7 {
		t.Fatalf("expected the claim to be finalized, got %+v", store.finalizedObjects)
	}
	if report.ObjectsDeleted != 1 || report.Failed != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimRefusesToDeleteAnObjectThatIsNotACheckpoint(t *testing.T) {
	claim := deletableClaim(t, 7, 41)
	snapshotRef, err := hangar.NewObjectRef(hangar.KindSnapshot, claim.Object.Digest, claim.Object.Generation)
	if err != nil {
		t.Fatalf("snapshot ref: %v", err)
	}
	claim.Object = snapshotRef
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{claim}}}
	objects := &durableObjectStub{}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if err == nil {
		t.Fatal("expected a foreign-kind claim to be reported")
	}
	if len(objects.deleted) != 0 {
		t.Fatalf("a foreign-kind object was deleted: %+v", objects.deleted)
	}
	if len(store.finalizedObjects) != 0 {
		t.Fatalf("a foreign-kind claim was finalized: %+v", store.finalizedObjects)
	}
	if report.ObjectsDeleted != 0 || report.Failed != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimKeepsTheRowWhenTheDurableDeleteFails(t *testing.T) {
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{deletableClaim(t, 7, 41)}}}
	objects := &durableObjectStub{delete: func(hangar.ObjectRef) error { return hangar.ErrConflict }}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if !errors.Is(err, hangar.ErrConflict) {
		t.Fatalf("expected the conflict to be reported, got %v", err)
	}
	if len(store.finalizedObjects) != 0 {
		t.Fatalf("the row was dropped despite a failed delete: %+v", store.finalizedObjects)
	}
	if report.ObjectsDeleted != 0 || report.Failed != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimInspectsAnAbandonedUploadInsteadOfDeletingIt(t *testing.T) {
	claim := uploadingClaim(t, 9)
	landed, err := hangar.NewObjectRef(hangar.KindCheckpoint, claim.UploadTicket.Digest, 12)
	if err != nil {
		t.Fatalf("landed ref: %v", err)
	}
	store := &reclaimStoreStub{
		objects:   [][]ObjectDeleteClaim{{claim}},
		reconcile: func(ObjectDeleteClaim, *hangar.ObjectRef) (bool, error) { return true, nil },
	}
	objects := &durableObjectStub{
		inspect: func(hangar.Kind, hangar.Digest) (hangar.ObjectRef, error) { return landed, nil },
	}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(objects.deleted) != 0 {
		t.Fatalf("an abandoned upload was deleted rather than reconciled: %+v", objects.deleted)
	}
	if len(store.reconciled) != 1 || store.reconciledObserved[0] == nil || *store.reconciledObserved[0] != landed {
		t.Fatalf("the landed object was not reported to the reconciler: %+v", store.reconciledObserved)
	}
	if len(store.finalizedObjects) != 0 {
		t.Fatalf("an upload claim was finalized as a deletion: %+v", store.finalizedObjects)
	}
	if report.UploadsAdopted != 1 || report.Failed != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimReportsAnUploadThatNeverLandedAsAbsent(t *testing.T) {
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{uploadingClaim(t, 9)}}}
	objects := &durableObjectStub{
		inspect: func(hangar.Kind, hangar.Digest) (hangar.ObjectRef, error) { return hangar.ObjectRef{}, hangar.ErrNotFound },
	}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(store.reconciled) != 1 || store.reconciledObserved[0] != nil {
		t.Fatalf("absence was not reported to the reconciler: %+v", store.reconciledObserved)
	}
	if report.UploadsDeferred != 1 || report.UploadsForgotten != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimCountsAForgottenUploadOnlyWhenTheReconcilerRetiresIt(t *testing.T) {
	store := &reclaimStoreStub{
		objects:   [][]ObjectDeleteClaim{{uploadingClaim(t, 9)}},
		reconcile: func(ObjectDeleteClaim, *hangar.ObjectRef) (bool, error) { return true, nil },
	}
	objects := &durableObjectStub{
		inspect: func(hangar.Kind, hangar.Digest) (hangar.ObjectRef, error) { return hangar.ObjectRef{}, hangar.ErrNotFound },
	}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if report.UploadsForgotten != 1 || report.UploadsDeferred != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimNeverSendsAnInspectionThatTheClaimDidNotIdentify(t *testing.T) {
	claim := uploadingClaim(t, 9)
	claim.UploadTicket.Kind = hangar.KindSnapshot
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{claim}}}
	objects := &durableObjectStub{}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if err == nil {
		t.Fatal("expected a foreign-kind upload ticket to be reported")
	}
	if len(objects.inspected) != 0 {
		t.Fatalf("a foreign-kind digest was inspected: %+v", objects.inspected)
	}
	if len(store.reconciled) != 0 {
		t.Fatalf("a foreign-kind claim was reconciled: %+v", store.reconciled)
	}
	if report.Failed != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimContinuesPastOneFailedObject(t *testing.T) {
	first, second := deletableClaim(t, 7, 41), deletableClaim(t, 8, 42)
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{first, second}}}
	objects := &durableObjectStub{delete: func(ref hangar.ObjectRef) error {
		if ref == first.Object {
			return errReclaimTest
		}
		return nil
	}}

	report, err := newTestReclaimer(t, store, objects).Collect(context.Background())
	if !errors.Is(err, errReclaimTest) {
		t.Fatalf("expected the first failure to be reported, got %v", err)
	}
	if report.ObjectsScanned != 2 || report.ObjectsDeleted != 1 || report.Failed != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	if len(store.finalizedObjects) != 1 || store.finalizedObjects[0].ObjectID != 8 {
		t.Fatalf("the healthy claim was not reclaimed: %+v", store.finalizedObjects)
	}
}

func TestReclaimExpiresCheckpointsSoTheirObjectsCanBecomeUnreferenced(t *testing.T) {
	claims := []ExpirationClaim{
		{CheckpointID: 3, HeadID: 1, Generation: 2, Token: "expire-a"},
		{CheckpointID: 4, HeadID: 1, Generation: 3, Token: "expire-b"},
	}
	store := &reclaimStoreStub{expirations: [][]ExpirationClaim{claims}}

	report, err := newTestReclaimer(t, store, &durableObjectStub{}).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(store.finalizedExpirations) != 2 {
		t.Fatalf("expected both expirations to be finalized, got %+v", store.finalizedExpirations)
	}
	if report.CheckpointsExpired != 2 || report.Failed != 0 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimRetiresTerminalCheckpointMetadata(t *testing.T) {
	store := &reclaimStoreStub{metadata: 5}

	report, err := newTestReclaimer(t, store, &durableObjectStub{}).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if report.MetadataRemoved != 5 {
		t.Fatalf("unexpected report: %+v", report)
	}
}

func TestReclaimStillReleasesObjectsWhenTheExpirationPhaseFails(t *testing.T) {
	store := &reclaimStoreStub{
		expirationErr: errReclaimTest,
		objects:       [][]ObjectDeleteClaim{{deletableClaim(t, 7, 41)}},
		metadata:      2,
	}

	report, err := newTestReclaimer(t, store, &durableObjectStub{}).Collect(context.Background())
	if !errors.Is(err, errReclaimTest) {
		t.Fatalf("expected the expiration failure to be reported, got %v", err)
	}
	if report.ObjectsDeleted != 1 || report.MetadataRemoved != 2 {
		t.Fatalf("a failed phase stopped the later phases: %+v", report)
	}
}

func TestReclaimStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &reclaimStoreStub{objects: [][]ObjectDeleteClaim{{deletableClaim(t, 7, 41)}}}
	objects := &durableObjectStub{}

	_, err := newTestReclaimer(t, store, objects).Collect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if len(objects.deleted) != 0 {
		t.Fatalf("a cancelled pass still deleted durable content: %+v", objects.deleted)
	}
}

// --- test doubles ---

type reclaimStoreStub struct {
	mutex sync.Mutex

	expirations   [][]ExpirationClaim
	expirationErr error
	objects       [][]ObjectDeleteClaim
	objectsErr    error
	metadata      int
	metadataErr   error

	finalizeExpirationErr func(ExpirationClaim) error
	finalizeObjectErr     func(ObjectDeleteClaim) error
	reconcile             func(ObjectDeleteClaim, *hangar.ObjectRef) (bool, error)

	finalizedExpirations []ExpirationClaim
	finalizedObjects     []ObjectDeleteClaim
	reconciled           []ObjectDeleteClaim
	reconciledObserved   []*hangar.ObjectRef
	limits               []int
}

func (stub *reclaimStoreStub) ClaimCheckpointExpirations(_ context.Context, limit int) ([]ExpirationClaim, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.limits = append(stub.limits, limit)
	if stub.expirationErr != nil {
		return nil, stub.expirationErr
	}
	if len(stub.expirations) == 0 {
		return nil, nil
	}
	page := stub.expirations[0]
	stub.expirations = stub.expirations[1:]
	return page, nil
}

func (stub *reclaimStoreStub) FinalizeCheckpointExpiration(_ context.Context, claim ExpirationClaim) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.finalizedExpirations = append(stub.finalizedExpirations, claim)
	if stub.finalizeExpirationErr != nil {
		return stub.finalizeExpirationErr(claim)
	}
	return nil
}

func (stub *reclaimStoreStub) ClaimUnreferencedObjects(_ context.Context, limit int) ([]ObjectDeleteClaim, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.limits = append(stub.limits, limit)
	if stub.objectsErr != nil {
		return nil, stub.objectsErr
	}
	if len(stub.objects) == 0 {
		return nil, nil
	}
	page := stub.objects[0]
	stub.objects = stub.objects[1:]
	return page, nil
}

func (stub *reclaimStoreStub) ReconcileUnreferencedUploadingObject(_ context.Context, claim ObjectDeleteClaim, observed *hangar.ObjectRef) (bool, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.reconciled = append(stub.reconciled, claim)
	stub.reconciledObserved = append(stub.reconciledObserved, observed)
	if stub.reconcile != nil {
		return stub.reconcile(claim, observed)
	}
	return false, nil
}

func (stub *reclaimStoreStub) FinalizeObjectDeletion(_ context.Context, claim ObjectDeleteClaim) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.finalizedObjects = append(stub.finalizedObjects, claim)
	if stub.finalizeObjectErr != nil {
		return stub.finalizeObjectErr(claim)
	}
	return nil
}

func (stub *reclaimStoreStub) CleanupTerminalMetadata(_ context.Context, limit int) (int, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.limits = append(stub.limits, limit)
	return stub.metadata, stub.metadataErr
}

type durableObjectStub struct {
	mutex sync.Mutex

	inspect func(hangar.Kind, hangar.Digest) (hangar.ObjectRef, error)
	delete  func(hangar.ObjectRef) error

	inspected []hangar.Digest
	deleted   []hangar.ObjectRef
}

func (stub *durableObjectStub) InspectObject(_ context.Context, kind hangar.Kind, digest hangar.Digest) (hangar.ObjectRef, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.inspected = append(stub.inspected, digest)
	if stub.inspect != nil {
		return stub.inspect(kind, digest)
	}
	return hangar.ObjectRef{}, hangar.ErrNotFound
}

func (stub *durableObjectStub) DeleteObject(_ context.Context, ref hangar.ObjectRef) error {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.deleted = append(stub.deleted, ref)
	if stub.delete != nil {
		return stub.delete(ref)
	}
	return nil
}

// --- helpers ---

func newTestReclaimer(t *testing.T, store ReclaimStore, objects DurableObjectStore) *Reclaimer {
	t.Helper()
	reclaimer, err := NewReclaimer(store, objects)
	if err != nil {
		t.Fatalf("new reclaimer: %v", err)
	}
	return reclaimer
}

func reclaimTestDigest(seed int) hangar.Digest {
	return hangar.Digest("sha256:" + strings.Repeat(fmt.Sprintf("%x", seed%16), 64))
}

func deletableClaim(t *testing.T, objectID int64, generation int64) ObjectDeleteClaim {
	t.Helper()
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, reclaimTestDigest(int(objectID)), generation)
	if err != nil {
		t.Fatalf("object ref: %v", err)
	}
	return ObjectDeleteClaim{ObjectID: objectID, Object: ref, Token: "delete-token"}
}

func uploadingClaim(t *testing.T, objectID int64) ObjectDeleteClaim {
	t.Helper()
	digest := reclaimTestDigest(int(objectID))
	key, err := hangar.Key(hangar.KindCheckpoint, digest)
	if err != nil {
		t.Fatalf("object key: %v", err)
	}
	return ObjectDeleteClaim{
		ObjectID:              objectID,
		Token:                 "reconcile-token",
		NeedsUploadInspection: true,
		UploadTicket: ObjectUploadTicket{
			ObjectID: objectID, StagedCheckpointID: objectID, Kind: hangar.KindCheckpoint,
			Digest: digest, Key: key, UploadToken: "upload-token",
		},
	}
}

var errReclaimTest = errors.New("reclaim test failure")
