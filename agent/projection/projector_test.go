package projection_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/snapshot"
)

type fakeCreator struct {
	uploaded snapshot.Snapshot
	sealed   map[string]snapshot.SealedOutput
	err      error
}

func (creator *fakeCreator) Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error) {
	return creator.uploaded, creator.err
}

func (creator *fakeCreator) Seal(context.Context, snapshot.SealRequest) (map[string]snapshot.SealedOutput, error) {
	return creator.sealed, creator.err
}

type recordingHandler struct {
	typeRef        snapshot.TypeRef
	candidates     []snapshot.SnapshotRef
	candidateCalls int
	project        func(context.Context, snapshot.SnapshotRef) error
	mu             sync.Mutex
	projected      []snapshot.SnapshotRef
}

func (h *recordingHandler) SnapshotType() snapshot.TypeRef { return h.typeRef }

func (h *recordingHandler) Candidates(context.Context, int) ([]snapshot.SnapshotRef, error) {
	h.mu.Lock()
	h.candidateCalls++
	h.mu.Unlock()
	return append([]snapshot.SnapshotRef(nil), h.candidates...), nil
}

func (h *recordingHandler) Project(ctx context.Context, ref snapshot.SnapshotRef) error {
	h.mu.Lock()
	h.projected = append(h.projected, ref)
	h.mu.Unlock()
	if h.project != nil {
		return h.project(ctx, ref)
	}
	return nil
}

func TestRegistryDispatchesOnlyByExactSnapshotType(t *testing.T) {
	review := &recordingHandler{typeRef: snapshot.TypeRef("review/v1")}
	registry, err := projection.NewRegistry([]projection.Handler{review})
	if err != nil {
		t.Fatal(err)
	}

	ref := snapshot.SnapshotRef{ID: 7, Type: "review/v1", Digest: testDigest('a')}
	if err := registry.Project(context.Background(), ref); err != nil {
		t.Fatalf("project exact type: %v", err)
	}
	if len(review.projected) != 1 || review.projected[0] != ref {
		t.Fatalf("projected = %#v, want exact ref", review.projected)
	}

	wrong := ref
	wrong.Type = "review/v2"
	if err := registry.Project(context.Background(), wrong); !errors.Is(err, snapshot.ErrUnsupportedType) {
		t.Fatalf("wrong type error = %v, want ErrUnsupportedType", err)
	}
	if len(review.projected) != 1 {
		t.Fatalf("wrong type reached review projector: %#v", review.projected)
	}
}

func TestRegistryRejectsDuplicateExactRegistration(t *testing.T) {
	_, err := projection.NewRegistry([]projection.Handler{
		&recordingHandler{typeRef: "review/v1"},
		&recordingHandler{typeRef: "review/v1"},
	})
	if err == nil {
		t.Fatal("duplicate registration succeeded")
	}
}

func TestReconcileRetriesEveryUnprojectedSnapshotAndContinuesPastFailure(t *testing.T) {
	first := snapshot.SnapshotRef{ID: 1, Type: "review/v1", Digest: testDigest('a')}
	second := snapshot.SnapshotRef{ID: 2, Type: "review/v1", Digest: testDigest('b')}
	attempts := map[snapshot.SnapshotID]int{}
	handler := &recordingHandler{
		typeRef:    "review/v1",
		candidates: []snapshot.SnapshotRef{first, second},
		project: func(_ context.Context, ref snapshot.SnapshotRef) error {
			attempts[ref.ID]++
			if ref.ID == first.ID && attempts[ref.ID] == 1 {
				return errors.New("temporarily unavailable")
			}
			return nil
		},
	}
	registry, err := projection.NewRegistry([]projection.Handler{handler})
	if err != nil {
		t.Fatal(err)
	}

	if err := registry.Reconcile(context.Background(), 100); err == nil {
		t.Fatal("first reconcile error = nil, want failed candidate reported")
	}
	if attempts[first.ID] != 1 || attempts[second.ID] != 1 {
		t.Fatalf("first attempts = %#v, want both candidates attempted", attempts)
	}
	if err := registry.Reconcile(context.Background(), 100); err != nil {
		t.Fatalf("retry reconcile: %v", err)
	}
	if attempts[first.ID] != 2 || attempts[second.ID] != 2 {
		t.Fatalf("retry attempts = %#v, want candidates queried again", attempts)
	}
}

func TestTriggerProjectsAfterCallerContextEndsAndReportsFailure(t *testing.T) {
	reported := make(chan error, 1)
	projected := make(chan error, 1)
	handler := &recordingHandler{
		typeRef: "review/v1",
		project: func(ctx context.Context, _ snapshot.SnapshotRef) error {
			// Observe cancellation while Project is still executing. The registry
			// correctly cancels its timeout context after Project returns, so
			// inspecting that context later would be a race with cleanup.
			projected <- ctx.Err()
			return errors.New("projection failed")
		},
	}
	registry, err := projection.NewRegistry(
		[]projection.Handler{handler},
		projection.WithTimeout(time.Second),
		projection.WithErrorReporter(func(_ context.Context, err error) { reported <- err }),
	)
	if err != nil {
		t.Fatal(err)
	}

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	registry.Trigger(requestContext, snapshot.SnapshotRef{ID: 1, Type: "review/v1", Digest: testDigest('a')})

	select {
	case err := <-projected:
		if err != nil {
			t.Fatalf("async projection inherited canceled request context: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async projection did not run")
	}
	select {
	case err := <-reported:
		if err == nil || err.Error() == "" {
			t.Fatalf("reported error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("async projection failure was not reported")
	}
}

func TestTriggerBoundsDetachedWorkAndDeduplicatesTheSameSnapshot(t *testing.T) {
	started := make(chan snapshot.SnapshotRef, 3)
	release := make(chan struct{})
	handler := &recordingHandler{
		typeRef: "review/v1",
		project: func(_ context.Context, ref snapshot.SnapshotRef) error {
			started <- ref
			<-release
			return nil
		},
	}
	registry, err := projection.NewRegistry(
		[]projection.Handler{handler},
		projection.WithMaxConcurrentTriggers(2),
	)
	if err != nil {
		t.Fatal(err)
	}
	first := snapshot.SnapshotRef{ID: 1, Type: "review/v1", Digest: testDigest('a')}
	second := snapshot.SnapshotRef{ID: 2, Type: "review/v1", Digest: testDigest('b')}
	third := snapshot.SnapshotRef{ID: 3, Type: "review/v1", Digest: testDigest('c')}
	registry.Trigger(context.Background(), first)
	registry.Trigger(context.Background(), first)
	registry.Trigger(context.Background(), second)
	registry.Trigger(context.Background(), third)

	seen := map[snapshot.SnapshotID]int{}
	for range 2 {
		select {
		case ref := <-started:
			seen[ref.ID]++
		case <-time.After(2 * time.Second):
			t.Fatal("bounded trigger did not start two distinct projections")
		}
	}
	if seen[first.ID] != 1 || seen[second.ID] != 1 || seen[third.ID] != 0 {
		t.Fatalf("started = %#v, want first and second once", seen)
	}
	select {
	case ref := <-started:
		t.Fatalf("trigger exceeded concurrency bound with %#v", ref)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	deadline := time.Now().Add(2 * time.Second)
	for {
		registry.Trigger(context.Background(), third)
		select {
		case ref := <-started:
			if ref != third {
				t.Fatalf("retried projection = %#v, want third", ref)
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("dropped bounded trigger could not be retried")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestProjectingCreatorTriggersOnlyAfterSuccessfulSealCommit(t *testing.T) {
	projected := make(chan snapshot.SnapshotRef, 2)
	handler := &recordingHandler{
		typeRef: "review/v1",
		project: func(_ context.Context, ref snapshot.SnapshotRef) error {
			projected <- ref
			return nil
		},
	}
	registry, err := projection.NewRegistry([]projection.Handler{handler})
	if err != nil {
		t.Fatal(err)
	}
	ref := snapshot.SnapshotRef{ID: 501, Type: "review/v1", Digest: testDigest('e')}
	creator, err := projection.NewProjectingCreator(&fakeCreator{
		sealed: map[string]snapshot.SealedOutput{
			"review": {Port: snapshot.Port{Name: "review", Type: "review/v1"}, Snapshot: ref},
		},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Seal(context.Background(), snapshot.SealRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-projected:
		if got != ref {
			t.Fatalf("projected = %#v, want %#v", got, ref)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("successful seal did not trigger projection")
	}

	failed, err := projection.NewProjectingCreator(&fakeCreator{err: errors.New("commit failed")}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failed.Seal(context.Background(), snapshot.SealRequest{}); err == nil {
		t.Fatal("failed seal returned nil error")
	}
	select {
	case got := <-projected:
		t.Fatalf("failed seal triggered projection of %#v", got)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProjectingCreatorDoesNotStartAFullReconciliationForEveryCommit(t *testing.T) {
	exact := snapshot.SnapshotRef{ID: 501, Type: "review/v1", Digest: testDigest('e')}
	older := snapshot.SnapshotRef{ID: 499, Type: "review/v1", Digest: testDigest('d')}
	projected := make(chan snapshot.SnapshotRef, 2)
	handler := &recordingHandler{
		typeRef:    "review/v1",
		candidates: []snapshot.SnapshotRef{older},
		project: func(_ context.Context, ref snapshot.SnapshotRef) error {
			projected <- ref
			return nil
		},
	}
	registry, err := projection.NewRegistry([]projection.Handler{handler})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := projection.NewProjectingCreator(&fakeCreator{uploaded: snapshot.Snapshot{
		ID: exact.ID, Type: exact.Type, Digest: exact.Digest,
	}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := creator.Upload(context.Background(), snapshot.UploadRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-projected:
		if got != exact {
			t.Fatalf("projected = %#v, want exact committed snapshot %#v", got, exact)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed snapshot was not projected")
	}
	time.Sleep(50 * time.Millisecond)
	handler.mu.Lock()
	candidateCalls := handler.candidateCalls
	handler.mu.Unlock()
	if candidateCalls != 0 {
		t.Fatalf("commit started %d global reconciliation queries, want none", candidateCalls)
	}
	select {
	case got := <-projected:
		t.Fatalf("commit also projected unrelated candidate %#v", got)
	default:
	}
}

func testDigest(char byte) snapshot.Digest {
	bytes := make([]byte, 64)
	for i := range bytes {
		bytes[i] = char
	}
	return snapshot.Digest("sha256:" + string(bytes))
}
