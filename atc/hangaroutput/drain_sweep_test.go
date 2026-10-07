package hangaroutput_test

// A6 and A7: taking the output plane out of service, and the orphan sweep.
//
// Both against the harness's real PostgreSQL, real daemon process and real
// GCS API emulator. A6 drains through the production capture coordinator and
// the production residue count; A7 runs the production sweep over objects
// planted in the emulator, deleting through the web's own delete client.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/reclaim"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// A6: with the flag off and one capture pending, admission refuses a new
// capture, the pending one still completes, and the residue reaches zero.
func TestA6AFlagOffWithOnePendingCaptureRefusesAdmissionCompletesThePendingAndDrains(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	pending := h.admit(t).produce(t, "written before the drain\n")

	// The drain's first step: the web's startup write with webEnabled off.
	moved, err := db.SetHangarEnabled(ctx, h.Conn, false)
	if err != nil {
		t.Fatalf("taking the plane out of service: %v", err)
	}
	if !moved {
		t.Fatal("the in-service row did not move")
	}

	// Admission refuses: a new capture is not inserted.
	_, err = h.Coordinator.Insert(ctx, output.PendingCapture{
		Execution: executioncontrol.Identity{ExecutionID: executioncontrol.ExecutionID(uuid.NewString()), Fence: 1},
		Output:    "result",
		Node:      harnessNodeName,
		NodeUID:   harnessNode,
		Term:      24 * time.Hour,
	})
	if !errors.Is(err, output.ErrCaptureDisabled) {
		t.Fatalf("a new capture was admitted out of service (err %v)", err)
	}

	status := readStatus(t, h)
	if status.Enabled {
		t.Error("the status reports the plane in service after the flag went off")
	}
	if status.Counts.PendingCaptures != 1 || status.Drained() {
		t.Fatalf("the pending capture is not residue: %+v", status.Counts)
	}

	// The pending capture completes: published, its marker released.
	ref := assertPublishedAndReleased(t, pending.advance(t))

	// Its claim is the consumer's to release; once released, nothing remains.
	tx, err := h.Conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := h.Repository.ReleaseClaim(ctx, db.HangarOutputTx{Tx: tx}, output.ClaimRelease{
		ProtocolVersion: output.ProtocolVersion, ClaimID: pending.Key().ClaimID(), Ref: ref,
		RequestedAt: output.NewTimestamp(time.Now()),
	}); err != nil {
		t.Fatalf("releasing the claim: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	status = readStatus(t, h)
	if residue := status.Counts.Residue(); residue != 0 {
		t.Fatalf("residue %d remains after the pending capture completed: %+v", residue, status.Counts)
	}
	if !status.Drained() {
		t.Error("out of service with zero residue is drained, and the status does not say so")
	}
}

// sweepFor is the production orphan sweep over the harness's bucket, with
// the web's own list and delete clients.
func sweepFor(t *testing.T, h *harness, captureDeadline time.Duration) (*reclaim.Sweep, *recordingDeleter) {
	t.Helper()

	namespace, err := output.DeriveNamespace(output.NamespaceConfig{
		Store:            output.StoreGCS,
		Bucket:           h.Bucket,
		DeploymentPrefix: "harness/one",
		TenantID:         "harness",
		ActivationEpoch:  harnessEpoch,
	})
	if err != nil {
		t.Fatalf("deriving the namespace: %v", err)
	}
	ctx := context.Background()
	lister, closeLister, err := hangargcs.NewClient(ctx, h.Store.URL())
	if err != nil {
		t.Fatalf("list client: %v", err)
	}
	t.Cleanup(func() { _ = closeLister() })
	deleter, closeDeleter, err := hangargcs.NewDeleteClient(ctx, h.Store.URL())
	if err != nil {
		t.Fatalf("delete client: %v", err)
	}
	t.Cleanup(func() { _ = closeDeleter() })
	recorder := &recordingDeleter{inner: deleter}
	deletes, err := reclaimer.New(namespace, reclaimer.Restrict(recorder))
	if err != nil {
		t.Fatalf("reclaimer: %v", err)
	}

	return &reclaim.Sweep{
		Transactor:      h.Coordinator.Transactor,
		Repository:      h.Repository,
		Namespace:       namespace,
		Lister:          lister,
		Reclaimer:       deletes,
		CaptureDeadline: captureDeadline,
		PageSize:        2,
	}, recorder
}

// plant puts an object straight into the emulator, created when the spec says.
func plant(t *testing.T, h *harness, key string, metadata map[string]string, created time.Time) int64 {
	t.Helper()

	h.Store.CreateObject(fakestorage.Object{
		ObjectAttrs: fakestorage.ObjectAttrs{
			BucketName: h.Bucket,
			Name:       key,
			Metadata:   metadata,
			Created:    created,
		},
		Content: []byte("planted " + key),
	})
	object, err := h.Store.GetObject(h.Bucket, key)
	if err != nil {
		t.Fatalf("reading back %s: %v", key, err)
	}

	return object.Generation
}

func markerFor(store string, scope hangar.Scope, digest hangar.Digest, created time.Time) map[string]string {
	return output.ObjectMarker{
		Version:         output.MarkerVersion,
		Scope:           scope,
		Digest:          digest,
		ReservationID:   output.ReservationID(uuid.NewString()),
		ActivationEpoch: harnessEpoch,
		Store:           store,
		CreatedAt:       output.NewTimestamp(created),
	}.Metadata()
}

func digestOf(fill byte) hangar.Digest {
	hex := make([]byte, 64)
	for i := range hex {
		hex[i] = fill
	}
	return hangar.Digest("sha256:" + string(hex))
}

// A7: the sweep deletes a this-store-marked orphan past twice the capture
// deadline by its exact generation, and leaves -- and counts -- a foreign-marked
// object, an unmarked object, a young orphan and a registered generation.
func TestA7TheOrphanSweepDeletesOnlyOldOrphansMarkedForThisStore(t *testing.T) {
	h := newHarness(t)
	const deadline = time.Hour
	sweep, recorder := sweepFor(t, h, deadline)
	namespace := sweep.Namespace
	store := namespace.StoreIdentity()

	old := time.Now().Add(-3 * deadline)
	young := time.Now().Add(-deadline)

	keyOf := func(digest hangar.Digest) string {
		key, err := namespace.ObjectKey(digest)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		return key
	}

	// A registered generation, published through the real capture path.
	registered := registeredRef(t, h)

	orphanKey := keyOf(digestOf('a'))
	orphanGeneration := plant(t, h, orphanKey, markerFor(store, namespace.Scope(), digestOf('a'), old), old)
	youngKey := keyOf(digestOf('b'))
	youngGeneration := plant(t, h, youngKey, markerFor(store, namespace.Scope(), digestOf('b'), young), young)
	foreignKey := keyOf(digestOf('c'))
	plant(t, h, foreignKey, markerFor("gs://another-deployment", namespace.Scope(), digestOf('c'), old), old)
	unmarkedKey := keyOf(digestOf('d'))
	plant(t, h, unmarkedKey, nil, old)

	// Another install in the same bucket, under the same prefix, with another
	// tenant: its own marker names its own store and scope, and it is foreign.
	neighbour, err := output.DeriveNamespace(output.NamespaceConfig{
		Store: output.StoreGCS, Bucket: h.Bucket, DeploymentPrefix: "harness/one",
		TenantID: "another-tenant", ActivationEpoch: harnessEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	neighbourKey, err := neighbour.ObjectKey(digestOf('e'))
	if err != nil {
		t.Fatal(err)
	}
	plant(t, h, neighbourKey, markerFor(neighbour.StoreIdentity(), neighbour.Scope(), digestOf('e'), old), old)
	// And one that claims this store's identity under the neighbour's scope.
	impostorKey, err := neighbour.ObjectKey(digestOf('f'))
	if err != nil {
		t.Fatal(err)
	}
	plant(t, h, impostorKey, markerFor(store, neighbour.Scope(), digestOf('f'), old), old)

	counts, err := sweep.Once(context.Background())
	if err != nil {
		t.Fatalf("the sweep: %v", err)
	}

	// The registered generation was published a moment ago, so it is young
	// here as well as registered; the second sweep below sees it old.
	expectCounts(t, counts, map[string]int{
		reclaim.SweepDeleted:  1,
		reclaim.SweepYoung:    2,
		reclaim.SweepForeign:  3,
		reclaim.SweepUnmarked: 1,
	})

	keys := map[string]bool{}
	for _, key := range h.bucketKeys(t) {
		keys[key] = true
	}
	if keys[orphanKey] {
		t.Errorf("the old orphan marked for this store survived (generation %d)", orphanGeneration)
	}
	for name, key := range map[string]string{"foreign": foreignKey, "unmarked": unmarkedKey, "young": youngKey} {
		if !keys[key] {
			t.Errorf("the %s object was deleted", name)
		}
	}
	registeredKey := keyOf(registered.Digest)
	if !keys[registeredKey] {
		t.Error("the registered generation was deleted by the sweep; reclamation owns it")
	}

	// The delete was of the exact listed generation, and the only delete.
	if len(recorder.deletes) != 1 || recorder.deletes[0] != (deleted{orphanKey, orphanGeneration}) {
		t.Errorf("the sweep deleted %v, want exactly %s at generation %d", recorder.deletes,
			orphanKey, orphanGeneration)
	}

	// Once everything is older than twice the deadline, the formerly young
	// orphan goes -- by its own generation -- and the registered generation,
	// the foreign object and the unmarked one still do not.
	sweep.CaptureDeadline = time.Millisecond
	again, err := sweep.Once(context.Background())
	if err != nil {
		t.Fatalf("the second sweep: %v", err)
	}
	expectCounts(t, again, map[string]int{
		reclaim.SweepDeleted:    1,
		reclaim.SweepRegistered: 1,
		reclaim.SweepForeign:    3,
		reclaim.SweepUnmarked:   1,
	})
	if len(recorder.deletes) != 2 || recorder.deletes[1] != (deleted{youngKey, youngGeneration}) {
		t.Errorf("the second sweep deleted %v, want %s at generation %d", recorder.deletes,
			youngKey, youngGeneration)
	}
	keys = map[string]bool{}
	for _, key := range h.bucketKeys(t) {
		keys[key] = true
	}
	for name, key := range map[string]string{"foreign": foreignKey, "unmarked": unmarkedKey, "registered": registeredKey,
		"another tenant's": neighbourKey, "scope-mismatched": impostorKey} {
		if !keys[key] {
			t.Errorf("the %s object was deleted by the second sweep", name)
		}
	}
}

func expectCounts(t *testing.T, counts, want map[string]int) {
	t.Helper()

	for _, class := range reclaim.SweepClasses() {
		if counts[class] != want[class] {
			t.Errorf("class %s: counted %d, want %d (all: %v)", class, counts[class], want[class], counts)
		}
	}
}

type deleted struct {
	key        string
	generation int64
}

// recordingDeleter is the web's delete client, recording what it was asked.
type recordingDeleter struct {
	inner   objectstore.DeleteClient
	deletes []deleted
}

func (r *recordingDeleter) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	return r.inner.StatExact(ctx, bucket, key, generation)
}

func (r *recordingDeleter) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	r.deletes = append(r.deletes, deleted{key, generation})
	return r.inner.DeleteExact(ctx, bucket, key, generation)
}

// failingLister answers its first pages and fails from failAt on.
type failingLister struct {
	inner  reclaim.Lister
	calls  int
	failAt int
}

func (lister *failingLister) List(ctx context.Context, bucket string, request objectstore.ListRequest) (objectstore.Page, error) {
	lister.calls++
	if lister.failAt > 0 && lister.calls >= lister.failAt {
		return objectstore.Page{}, errors.New("the store did not answer the list")
	}
	return lister.inner.List(ctx, bucket, request)
}

// A list that fails mid-pass fails the pass and keeps its progress: the next
// pass resumes after the last page it judged instead of silently starting
// over.
func TestAnOrphanSweepThatCannotListFailsAndResumesWhereItStopped(t *testing.T) {
	h := newHarness(t)
	sweep, _ := sweepFor(t, h, time.Hour)
	namespace := sweep.Namespace
	old := time.Now().Add(-3 * time.Hour)
	for _, fill := range []byte{'1', '2', '3', '4', '5'} {
		key, err := namespace.ObjectKey(digestOf(fill))
		if err != nil {
			t.Fatal(err)
		}
		plant(t, h, key, nil, old)
	}

	lister := &failingLister{inner: sweep.Lister, failAt: 2}
	sweep.Lister = lister
	counts, err := sweep.Once(context.Background())
	if err == nil {
		t.Fatal("a pass whose list failed reported success")
	}
	if counts[reclaim.SweepUnmarked] != 2 {
		t.Errorf("the first page was not counted before the failure: %v", counts)
	}

	lister.failAt = 0
	counts, err = sweep.Once(context.Background())
	if err != nil {
		t.Fatalf("the resumed pass: %v", err)
	}
	if counts[reclaim.SweepUnmarked] != 3 {
		t.Errorf("the resumed pass judged %d objects, want the 3 after the first page: %v",
			counts[reclaim.SweepUnmarked], counts)
	}
}
