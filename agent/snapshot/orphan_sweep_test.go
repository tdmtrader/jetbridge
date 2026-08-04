package snapshot

import (
	"context"
	"errors"
	"testing"
	"time"
)

const sweepDigestA = Digest("sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111")
const sweepDigestB = Digest("sha256:" +
	"2222222222222222222222222222222222222222222222222222222222222222")

type sweepInventory struct {
	objects []DurableObject
	listErr error

	deleted   []DurableObject
	deleteErr error
}

func (inventory *sweepInventory) ListDurableObjects(ctx context.Context, visit func(DurableObject) error) error {
	if inventory.listErr != nil {
		return inventory.listErr
	}
	for _, object := range inventory.objects {
		if err := visit(object); err != nil {
			return err
		}
	}
	return nil
}

func (inventory *sweepInventory) DeleteDurableObject(ctx context.Context, object DurableObject) error {
	if inventory.deleteErr != nil {
		return inventory.deleteErr
	}
	inventory.deleted = append(inventory.deleted, object)
	return nil
}

func sweepObject(digest Digest, age time.Duration, now time.Time) DurableObject {
	return DurableObject{
		Digest:     digest,
		Key:        "hangar/v1/snapshots/sha256/x.tar.zst",
		Generation: 7,
		Bytes:      1024,
		CreatedAt:  now.Add(-age),
	}
}

func newSweepLifecycle(t *testing.T, metadata *lifecycleMetadata, now time.Time) *Lifecycle {
	t.Helper()
	if metadata.states == nil {
		metadata.states = map[Digest]DigestState{}
	}
	// The real store always stamps the digest it was asked about, even when no
	// rows exist; mirror that so an absent digest yields a valid empty state.
	for _, digest := range []Digest{sweepDigestA, sweepDigestB} {
		state := metadata.states[digest]
		state.Digest = digest
		metadata.states[digest] = state
	}
	return mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
}

func TestSweepOrphansReclaimsUnreferencedObjectPastThreshold(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{
		sweepObject(sweepDigestA, 48*time.Hour, now),
	}}

	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if len(inventory.deleted) != 1 || inventory.deleted[0].Digest != sweepDigestA {
		t.Fatalf("deleted = %v, want the unreferenced object", inventory.deleted)
	}
	if inventory.deleted[0].Generation != 7 {
		t.Fatalf("delete generation = %d, want the generation that was judged", inventory.deleted[0].Generation)
	}
	if report.Scanned != 1 || report.OrphansReclaimed != 1 || report.OrphansReclaimable != 1 {
		t.Fatalf("report = %+v", report)
	}
	if report.OrphanBytes != 1024 {
		t.Fatalf("OrphanBytes = %d, want 1024", report.OrphanBytes)
	}
}

// An upload in flight has bytes before it has a row, so recency alone must
// retain an object that metadata cannot yet explain.
func TestSweepOrphansRetainsObjectYoungerThanThreshold(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{
		sweepObject(sweepDigestA, DefaultOrphanSweepAge-time.Minute, now),
	}}

	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if len(inventory.deleted) != 0 {
		t.Fatalf("sweep deleted a young object: %v", inventory.deleted)
	}
	if report.Scanned != 1 || report.Deferred != 1 || report.OrphansReclaimed != 0 {
		t.Fatalf("report = %+v", report)
	}
	// A young object must not consult metadata at all: the age gate is the
	// outermost defense and has to hold on its own.
	if metadata.digestStateCalls[sweepDigestA] != 0 {
		t.Fatalf("young object consulted metadata %d times", metadata.digestStateCalls[sweepDigestA])
	}
}

// The core safety property: a digest metadata knows anything about is retained,
// no matter which table knows it or what state it is in.
func TestSweepOrphansNeverDeletesReferencedObjectInAnyState(t *testing.T) {
	now := time.Now().UTC()
	for name, state := range map[string]DigestState{
		"available snapshot": {
			Snapshots: []Snapshot{lifecycleManifest(sweepDigestA, ContentStateAvailable)},
		},
		// Expired is the sharpest case: content the platform has already given
		// up on is still a row, and a row still means "not ours to delete here".
		"expired snapshot": {
			Snapshots: []Snapshot{lifecycleManifest(sweepDigestA, ContentStateExpired)},
		},
		"staged upload only": {
			Stages: []StagedUpload{lifecycleStage(1, sweepDigestA, now)},
		},
		"location row only": {
			Locations: []Location{{
				Digest: sweepDigestA, Driver: "hangar-v1",
				Key: "hangar/v1/snapshots/sha256/x.tar.zst",
			}},
		},
		"snapshot and stage together": {
			Snapshots: []Snapshot{lifecycleManifest(sweepDigestA, ContentStateAvailable)},
			Stages:    []StagedUpload{lifecycleStage(1, sweepDigestA, now)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			metadata := &lifecycleMetadata{states: map[Digest]DigestState{sweepDigestA: state}}
			lifecycle := newSweepLifecycle(t, metadata, now)
			inventory := &sweepInventory{objects: []DurableObject{
				// Far past any threshold, so only the metadata check can save it.
				sweepObject(sweepDigestA, 365*24*time.Hour, now),
			}}

			report, err := lifecycle.SweepOrphans(
				context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge,
			)
			if err != nil {
				t.Fatalf("SweepOrphans: %v", err)
			}
			if len(inventory.deleted) != 0 {
				t.Fatalf("sweep deleted a referenced object: %v", inventory.deleted)
			}
			if report.OrphansReclaimed != 0 || report.OrphansReclaimable != 0 {
				t.Fatalf("report claimed a referenced object: %+v", report)
			}
			if report.Deferred != 1 {
				t.Fatalf("report = %+v, want the referenced object deferred", report)
			}
		})
	}
}

// Report mode has to identify exactly what reclaim mode would remove, or it is
// useless as a pre-authorization inspection.
func TestSweepOrphansReportModeIdentifiesWithoutDeleting(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{
		sweepDigestB: {Snapshots: []Snapshot{lifecycleManifest(sweepDigestB, ContentStateAvailable)}},
	}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{
		sweepObject(sweepDigestA, 48*time.Hour, now),
		sweepObject(sweepDigestB, 48*time.Hour, now),
	}}

	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReport, DefaultOrphanSweepAge)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if len(inventory.deleted) != 0 {
		t.Fatalf("report mode deleted: %v", inventory.deleted)
	}
	if report.Scanned != 2 || report.OrphansReclaimable != 1 || report.OrphansReclaimed != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.OrphanBytes != 1024 {
		t.Fatalf("OrphanBytes = %d, want the reclaimable object's size", report.OrphanBytes)
	}
}

func TestSweepOrphansOffModeTouchesNothing(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{sweepObject(sweepDigestA, 48*time.Hour, now)}}

	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepOff, DefaultOrphanSweepAge)
	if err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if report.Scanned != 0 || len(inventory.deleted) != 0 {
		t.Fatalf("off mode acted: report=%+v deleted=%v", report, inventory.deleted)
	}
}

// The age threshold is the last defense that does not depend on metadata being
// correct, so it must not be configurable down to a racy value.
func TestSweepOrphansRejectsUnsafeConfiguration(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{sweepObject(sweepDigestA, 365*24*time.Hour, now)}}

	if _, err := lifecycle.SweepOrphans(
		context.Background(), inventory, OrphanSweepReclaim, MinOrphanSweepAge-time.Second,
	); err == nil {
		t.Fatal("sweep accepted an age below the safe floor")
	}
	if _, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReclaim, 0); err == nil {
		t.Fatal("sweep accepted a zero age threshold")
	}
	if _, err := lifecycle.SweepOrphans(
		context.Background(), inventory, OrphanSweepMode("bogus"), DefaultOrphanSweepAge,
	); err == nil {
		t.Fatal("sweep accepted an invalid mode")
	}
	if _, err := lifecycle.SweepOrphans(
		context.Background(), nil, OrphanSweepReclaim, DefaultOrphanSweepAge,
	); err == nil {
		t.Fatal("sweep accepted a missing inventory")
	}
	if len(inventory.deleted) != 0 {
		t.Fatalf("rejected configuration still deleted: %v", inventory.deleted)
	}
}

// An object the sweep cannot fully describe — no creation time, no generation —
// cannot be aged or pinned, so it must be surfaced rather than deleted.
func TestSweepOrphansRefusesUnusableObjects(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)

	missingCreatedAt := sweepObject(sweepDigestA, 48*time.Hour, now)
	missingCreatedAt.CreatedAt = time.Time{}
	missingGeneration := sweepObject(sweepDigestB, 48*time.Hour, now)
	missingGeneration.Generation = 0

	inventory := &sweepInventory{objects: []DurableObject{missingCreatedAt, missingGeneration}}
	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge)
	if err == nil {
		t.Fatal("sweep silently accepted unusable objects")
	}
	if len(inventory.deleted) != 0 {
		t.Fatalf("sweep deleted an unusable object: %v", inventory.deleted)
	}
	if report.Failed != 2 || report.Scanned != 2 {
		t.Fatalf("report = %+v", report)
	}
}

// One digest failing must not abandon the rest of the pass, and must not be
// counted as reclaimed.
func TestSweepOrphansReportsPerObjectFailures(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{
		states: map[Digest]DigestState{},
		digestState: func(_ context.Context, _ DigestLease, digest Digest, _ time.Time) (DigestState, error) {
			if digest == sweepDigestA {
				return DigestState{}, errors.New("metadata unavailable")
			}
			return DigestState{Digest: digest}, nil
		},
	}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{
		sweepObject(sweepDigestA, 48*time.Hour, now),
		sweepObject(sweepDigestB, 48*time.Hour, now),
	}}

	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge)
	if err == nil {
		t.Fatal("sweep hid a per-object failure")
	}
	if report.Failed != 1 || report.OrphansReclaimed != 1 {
		t.Fatalf("report = %+v, want one failure and the healthy digest still reclaimed", report)
	}
	if len(inventory.deleted) != 1 || inventory.deleted[0].Digest != sweepDigestB {
		t.Fatalf("deleted = %v", inventory.deleted)
	}
}

// A delete that fails must not be reported as reclaimed, or the operator's only
// record of what was removed becomes fiction.
func TestSweepOrphansDoesNotClaimFailedDeletes(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{
		objects:   []DurableObject{sweepObject(sweepDigestA, 48*time.Hour, now)},
		deleteErr: errors.New("generation conflict"),
	}

	report, err := lifecycle.SweepOrphans(context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge)
	if err == nil {
		t.Fatal("sweep hid a failed delete")
	}
	if report.OrphansReclaimed != 0 || report.OrphanBytes != 0 {
		t.Fatalf("report = %+v, want nothing claimed", report)
	}
	if report.Failed != 1 {
		t.Fatalf("report = %+v, want the failed delete counted", report)
	}
}

func TestSweepOrphansPropagatesListingFailure(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{listErr: errors.New("store unreachable")}

	if _, err := lifecycle.SweepOrphans(
		context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge,
	); err == nil {
		t.Fatal("sweep hid a listing failure")
	}
}

func TestSweepOrphansHonorsCancellation(t *testing.T) {
	now := time.Now().UTC()
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{}}
	lifecycle := newSweepLifecycle(t, metadata, now)
	inventory := &sweepInventory{objects: []DurableObject{sweepObject(sweepDigestA, 48*time.Hour, now)}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lifecycle.SweepOrphans(ctx, inventory, OrphanSweepReclaim, DefaultOrphanSweepAge); !errors.Is(err, context.Canceled) {
		t.Fatalf("SweepOrphans error = %v, want context.Canceled", err)
	}
	if len(inventory.deleted) != 0 {
		t.Fatalf("cancelled sweep deleted: %v", inventory.deleted)
	}
}

// The state check has to happen under the digest lease, because that is the
// same lease the sealer holds across stage, upload, and commit.
func TestSweepOrphansChecksStateUnderDigestLease(t *testing.T) {
	now := time.Now().UTC()
	var leased bool
	metadata := &lifecycleMetadata{
		states: map[Digest]DigestState{},
		digestState: func(_ context.Context, lease DigestLease, digest Digest, _ time.Time) (DigestState, error) {
			leased = lease != nil && lease.Covers(digest)
			return DigestState{Digest: digest}, nil
		},
	}
	locks := &lifecycleLocks{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, locks, now)
	inventory := &sweepInventory{objects: []DurableObject{sweepObject(sweepDigestA, 48*time.Hour, now)}}

	if _, err := lifecycle.SweepOrphans(
		context.Background(), inventory, OrphanSweepReclaim, DefaultOrphanSweepAge,
	); err != nil {
		t.Fatalf("SweepOrphans: %v", err)
	}
	if !leased {
		t.Fatal("digest state was read without a covering lease")
	}
	if len(locks.calls) != 1 || len(locks.calls[0]) != 1 || locks.calls[0][0] != sweepDigestA {
		t.Fatalf("lock acquisitions = %v", locks.calls)
	}
}
