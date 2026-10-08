package hangaroutput_test

// The reclaim pass: one refcount and one action.
//
// A registered generation nothing holds -- no live claim, no capture of its
// tree in flight, grace elapsed -- is deleted by its exact generation and the
// lifecycle row is stamped reclaimed, in the transaction that held the row
// across the delete. Against the harness's real PostgreSQL, real daemon and
// real GCS emulator, with the web's own delete client; the ONE thing stood in
// for is the store's ANSWER to a delete, so the pass can be shown each outcome
// the reclaimer names.

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"code.cloudfoundry.org/lager/v3/lagertest"

	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/hangaroutput/reclaim"
	"github.com/concourse/concourse/hangar"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// reclaimPassFor is the production reclaim pass over the harness's bucket,
// with the web's own delete client under a recorder.
func reclaimPassFor(t *testing.T, h *harness, grace time.Duration) (*reclaim.Pass, *recordingDeleter) {
	t.Helper()

	deleter, closeDeleter, err := hangargcs.NewDeleteClient(context.Background(), h.Store.URL())
	if err != nil {
		t.Fatalf("delete client: %v", err)
	}
	t.Cleanup(func() { _ = closeDeleter() })
	recorder := &recordingDeleter{inner: deleter}

	return reclaimPassOver(t, h, grace, recorder), recorder
}

// reclaimPassOver is the pass over any store: the recorder, or one that
// answers what the spec says.
func reclaimPassOver(t *testing.T, h *harness, grace time.Duration, store reclaimer.Store) *reclaim.Pass {
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
	deletes, err := reclaimer.New(namespace, store)
	if err != nil {
		t.Fatalf("reclaimer: %v", err)
	}

	return &reclaim.Pass{
		Transactor: h.Coordinator.Transactor,
		Repository: h.Repository,
		Reclaimer:  deletes,
		Grace:      grace,
	}
}

// unheldRef publishes one generation and gives its capture's claim back, so
// nothing holds it; after the pass's grace it is reclaimable.
func unheldRef(t *testing.T, h *harness, content string) hangar.TreeRef {
	t.Helper()

	c := h.admit(t).produce(t, content)
	ref := assertPublishedAndReleased(t, c.advance(t))
	releaseClaim(t, h, c.Key().ClaimID(), ref)
	// Grace is measured on the database clock from registered_at; the passes
	// below use a millisecond.
	time.Sleep(10 * time.Millisecond)

	return ref
}

// answeringStore is a delete client that answers what it is told, after the
// real delete or instead of it. A nil answer means the real store answers.
type answeringStore struct {
	inner  reclaimer.Store
	answer error
	asked  []deleted
}

func (store *answeringStore) StatExact(ctx context.Context, bucket, key string, generation int64) (objectstore.Attrs, error) {
	return store.inner.StatExact(ctx, bucket, key, generation)
}

func (store *answeringStore) DeleteExact(ctx context.Context, bucket, key string, generation int64) error {
	store.asked = append(store.asked, deleted{key, generation})
	if store.answer != nil {
		return store.answer
	}

	return store.inner.DeleteExact(ctx, bucket, key, generation)
}

func realDeleter(t *testing.T, h *harness) objectstore.DeleteClient {
	t.Helper()

	deleter, closeDeleter, err := hangargcs.NewDeleteClient(context.Background(), h.Store.URL())
	if err != nil {
		t.Fatalf("delete client: %v", err)
	}
	t.Cleanup(func() { _ = closeDeleter() })

	return deleter
}

func expectPass(t *testing.T, pass *reclaim.Pass, wantReclaimed, wantDeferred, wantFailed int) error {
	t.Helper()

	reclaimed, deferred, failed, err := pass.Reclaim(context.Background())
	if reclaimed != wantReclaimed || deferred != wantDeferred || failed != wantFailed {
		t.Fatalf("the pass reclaimed %d, deferred %d, failed %d (err %v); want %d, %d, %d",
			reclaimed, deferred, failed, err, wantReclaimed, wantDeferred, wantFailed)
	}

	return err
}

// The pass's own control: a confirmed delete of the exact generation stamps
// the row reclaimed, the object is gone, and a generation still inside grace
// is not a candidate at all.
func TestAConfirmedDeleteStampsTheGenerationReclaimed(t *testing.T) {
	h := newHarness(t)
	ref := unheldRef(t, h, "nothing holds this\n")

	patient, _ := reclaimPassFor(t, h, 24*time.Hour)
	if err := expectPass(t, patient, 0, 0, 0); err != nil {
		t.Fatalf("a pass with nothing past grace: %v", err)
	}
	if reclaimedAt(t, h, ref) != nil {
		t.Fatal("a generation inside its publication grace was stamped reclaimed")
	}

	pass, recorder := reclaimPassFor(t, h, time.Millisecond)
	if err := expectPass(t, pass, 1, 0, 0); err != nil {
		t.Fatalf("the pass: %v", err)
	}
	if reclaimedAt(t, h, ref) == nil {
		t.Error("the confirmed delete did not stamp the row reclaimed")
	}
	if keys := h.bucketKeys(t); len(keys) != 0 {
		t.Errorf("the object survived the pass: %v", keys)
	}
	if len(recorder.deletes) != 1 || recorder.deletes[0].generation != ref.Generation {
		t.Errorf("the pass deleted %v, want exactly generation %d", recorder.deletes, ref.Generation)
	}
	if status := readStatus(t, h); status.Counts.LiveGenerations != 0 {
		t.Errorf("a reclaimed generation is still counted live: %+v", status.Counts)
	}

	// Reclaimed is final: the next pass has nothing, and the store is not asked.
	if err := expectPass(t, pass, 0, 0, 0); err != nil {
		t.Fatalf("the pass after: %v", err)
	}
	if len(recorder.deletes) != 1 {
		t.Errorf("a reclaimed generation was deleted again: %v", recorder.deletes)
	}
}

// Already absent is the same fact as deleted: the exact generation is gone.
// A delete whose answer was lost last pass, or an object gone out of band,
// both land here, and the row is stamped.
func TestAnAlreadyAbsentGenerationIsStampedReclaimed(t *testing.T) {
	h := newHarness(t)
	ref := unheldRef(t, h, "gone before the pass asked\n")
	keys := h.bucketKeys(t)
	if len(keys) != 1 {
		t.Fatalf("want one object, have %v", keys)
	}
	if err := realDeleter(t, h).DeleteExact(context.Background(), h.Bucket, keys[0], ref.Generation); err != nil {
		t.Fatalf("removing the object behind the plane's back: %v", err)
	}

	pass, recorder := reclaimPassFor(t, h, time.Millisecond)
	if err := expectPass(t, pass, 1, 0, 0); err != nil {
		t.Fatalf("the pass: %v", err)
	}
	if reclaimedAt(t, h, ref) == nil {
		t.Error("an already-absent generation was not stamped reclaimed")
	}
	if len(recorder.deletes) != 1 {
		t.Errorf("the pass asked %v, want one conditional delete", recorder.deletes)
	}
}

// A generation conflict -- another generation at the key -- is reclaimed too:
// the exact generation is gone, the object there is somebody else's and the
// orphan sweep judges it by its own marker. It is logged, and never broadened
// into an unconditional delete: the pass asked for the registered generation
// once and nothing else, and the newer object survives.
//
// The store's answer is scripted: the emulator deletes a key whatever
// generation the precondition names, so it cannot produce the 412 a real
// store does. The reclaimer's translation of that answer is its own suite's.
func TestAGenerationConflictIsStampedReclaimedAndLoggedAndTheNewerObjectSurvives(t *testing.T) {
	h := newHarness(t)
	ref := unheldRef(t, h, "replaced at its key\n")
	keys := h.bucketKeys(t)
	if len(keys) != 1 {
		t.Fatalf("want one object, have %v", keys)
	}
	newer := plant(t, h, keys[0], nil, time.Now())
	if newer == ref.Generation {
		t.Fatalf("the replacement kept generation %d; the spec needs another one", newer)
	}

	logger := lagertest.NewTestLogger("reclaim")
	ctx := lagerctx.NewContext(context.Background(), logger)
	store := &answeringStore{inner: realDeleter(t, h), answer: objectstore.ErrPreconditionFailed}
	pass := reclaimPassOver(t, h, time.Millisecond, store)
	reclaimed, deferred, failed, err := pass.Reclaim(ctx)
	if err != nil || reclaimed != 1 || deferred != 0 || failed != 0 {
		t.Fatalf("the pass reclaimed %d, deferred %d, failed %d (err %v); want 1, 0, 0",
			reclaimed, deferred, failed, err)
	}
	if reclaimedAt(t, h, ref) == nil {
		t.Error("a generation conflict was not stamped reclaimed")
	}
	if len(store.asked) != 1 || store.asked[0] != (deleted{keys[0], ref.Generation}) {
		t.Errorf("the pass asked %v, want exactly the registered generation %d once and nothing broader",
			store.asked, ref.Generation)
	}
	object, err := h.Store.GetObject(h.Bucket, keys[0])
	if err != nil || object.Generation != newer {
		t.Errorf("the newer object at the key did not survive (generation %d, err %v)", newer, err)
	}
	if logs := logger.Buffer().Contents(); !bytes.Contains(logs, []byte("hangar-output-reclaim-generation-conflict")) {
		t.Errorf("the conflict was not logged: %s", logs)
	}

	// Reclaimed is final: the next pass does not ask again.
	if err := expectPass(t, pass, 0, 0, 0); err != nil {
		t.Fatalf("the pass after: %v", err)
	}
	if len(store.asked) != 1 {
		t.Errorf("a reclaimed generation was asked about again: %v", store.asked)
	}
}

// The store refusing the web's delete credential is the plane not being the
// plane that was configured. The pass records a runtime principal denial --
// which blocks admission until an operator resolves it -- and leaves the row
// registered: nothing was settled about the object.
func TestAnUnauthorizedDeleteRecordsAPrincipalDenialAndLeavesTheRowRegistered(t *testing.T) {
	h := newHarness(t)
	ref := unheldRef(t, h, "the store says no\n")

	store := &answeringStore{inner: realDeleter(t, h), answer: objectstore.ErrUnauthorized}
	pass := reclaimPassOver(t, h, time.Millisecond, store)
	err := expectPass(t, pass, 0, 0, 1)
	if !errors.Is(err, output.ErrUnauthorized) {
		t.Fatalf("the pass answered %v, want unauthorized", err)
	}
	if reclaimedAt(t, h, ref) != nil {
		t.Error("a refused delete stamped the row reclaimed")
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the object is gone: %v", keys)
	}

	status := readStatus(t, h)
	if !status.AtRisk || status.Violations[output.ViolationRuntimePrincipalDenied] != 1 {
		t.Fatalf("no runtime_principal_denied finding was recorded: %+v", status.Findings)
	}
	if status.Findings[0].Subject != string(output.PrincipalReclaimer) {
		t.Errorf("the finding names %q, want the reclaimer role", status.Findings[0].Subject)
	}

	// At risk, the next pass deletes nothing: the generation is deferred
	// until an operator resolves the finding, and the store is not asked.
	asked := len(store.asked)
	store.answer = nil
	if err := expectPass(t, pass, 0, 1, 0); err != nil {
		t.Fatalf("the pass under an open finding: %v", err)
	}
	if len(store.asked) != asked {
		t.Error("the pass asked the store to delete under an open blocking finding")
	}
}

// A delete that did not answer settles nothing: the transaction rolls back,
// the row stays registered, and the next pass asks again.
func TestAFailedDeleteLeavesTheRowRegisteredAndTheNextPassRetries(t *testing.T) {
	h := newHarness(t)
	ref := unheldRef(t, h, "the store did not answer\n")

	store := &answeringStore{inner: realDeleter(t, h), answer: errors.New("connection reset by peer")}
	pass := reclaimPassOver(t, h, time.Millisecond, store)
	err := expectPass(t, pass, 0, 0, 1)
	if err == nil || errors.Is(err, output.ErrUnauthorized) {
		t.Fatalf("a delete that did not answer was reported as %v", err)
	}
	if reclaimedAt(t, h, ref) != nil {
		t.Fatal("a delete that did not answer stamped the row reclaimed")
	}
	if status := readStatus(t, h); status.AtRisk {
		t.Errorf("a delete that did not answer opened a blocking finding: %+v", status.Findings)
	}
	if len(store.asked) != 1 {
		t.Fatalf("the pass asked %v, want one delete", store.asked)
	}

	// The store answers now; the next pass retries the same exact generation.
	store.answer = nil
	if err := expectPass(t, pass, 1, 0, 0); err != nil {
		t.Fatalf("the retrying pass: %v", err)
	}
	if len(store.asked) != 2 || store.asked[1] != (deleted{store.asked[0].key, ref.Generation}) {
		t.Errorf("the retry asked %v, want the same exact generation again", store.asked)
	}
	if reclaimedAt(t, h, ref) == nil {
		t.Error("the retried delete did not stamp the row reclaimed")
	}
	if keys := h.bucketKeys(t); len(keys) != 0 {
		t.Errorf("the object survived the retry: %v", keys)
	}
}

// A claim taken between the candidate query and the hold is found by the
// hold: the pass defers the generation, deletes nothing and stamps nothing.
// The candidate query is a bound on the work, not a decision; the decision
// is made under the tree and lifecycle locks.
func TestAClaimTakenBetweenTheCandidateQueryAndTheHoldDefersTheGeneration(t *testing.T) {
	h := newHarness(t)
	ref := unheldRef(t, h, "claimed at the last moment\n")

	pass, recorder := reclaimPassFor(t, h, time.Millisecond)
	// The pass's first transaction is the candidate query; its second is the
	// hold. A consumer's claim lands between them.
	hooked := &hookedTransactor{inner: pass.Transactor, at: 2, hook: func() {
		claimOn(t, h, ref)
	}}
	pass.Transactor = hooked

	if err := expectPass(t, pass, 0, 1, 0); err != nil {
		t.Fatalf("a deferred generation is not a failure: %v", err)
	}
	if hooked.begins < 2 {
		t.Fatalf("the pass opened %d transactions; the claim was never taken between the query and the hold",
			hooked.begins)
	}
	if reclaimedAt(t, h, ref) != nil {
		t.Error("a generation claimed under the pass was stamped reclaimed")
	}
	if len(recorder.deletes) != 0 {
		t.Errorf("a generation claimed under the pass was deleted: %v", recorder.deletes)
	}
	if keys := h.bucketKeys(t); len(keys) != 1 {
		t.Errorf("the claimed object is gone: %v", keys)
	}

	// And while the claim lives, the generation is not even a candidate.
	pass.Transactor = hooked.inner
	if err := expectPass(t, pass, 0, 0, 0); err != nil {
		t.Fatalf("the pass over a claimed generation: %v", err)
	}
}

// hookedTransactor runs hook just before the at-th Begin returns, which is
// how a spec puts a row between two of the pass's transactions.
type hookedTransactor struct {
	inner  hangaroutput.Transactor
	at     int
	hook   func()
	begins int
}

func (transactor *hookedTransactor) Begin() (hangaroutput.Transaction, error) {
	transactor.begins++
	if transactor.begins == transactor.at && transactor.hook != nil {
		transactor.hook()
	}

	return transactor.inner.Begin()
}
