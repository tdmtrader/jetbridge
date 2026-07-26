package snapshot

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type lifecycleMetadata struct {
	MetadataStore
	page              LifecycleCandidatePage
	discover          func(context.Context, LifecyclePageRequest) (LifecycleCandidatePage, error)
	discoveryRequests []LifecyclePageRequest
	states            map[Digest]DigestState
	digestState       func(context.Context, DigestLease, Digest, time.Time) (DigestState, error)
	digestStateCalls  map[Digest]int
	events            []string
	removeErr         error
	removeStages      func(context.Context, DigestLease, Digest, []int64) error
	removeLocation    func(context.Context, DigestLease, Location) error
	addLocation       func(context.Context, DigestLease, Location) error
	markResult        bool
	markErr           error
	markDigestExpired func(context.Context, DigestLease, Digest, time.Time) (bool, error)
	reapCutoffs       []time.Time
	reapCount         int
	reapErr           error
}

func (m *lifecycleMetadata) DiscoverLifecycleCandidates(ctx context.Context, request LifecyclePageRequest) (LifecycleCandidatePage, error) {
	m.discoveryRequests = append(m.discoveryRequests, request)
	if m.discover != nil {
		return m.discover(ctx, request)
	}
	return m.page, nil
}

func (m *lifecycleMetadata) DigestState(ctx context.Context, lease DigestLease, digest Digest, now time.Time) (DigestState, error) {
	if m.digestStateCalls == nil {
		m.digestStateCalls = map[Digest]int{}
	}
	m.digestStateCalls[digest]++
	if m.digestState != nil {
		return m.digestState(ctx, lease, digest, now)
	}
	return m.states[digest].Clone(), nil
}

func (m *lifecycleMetadata) RemoveStagedUploads(ctx context.Context, lease DigestLease, digest Digest, ids []int64) error {
	m.events = append(m.events, "remove-stages")
	if m.removeStages != nil {
		return m.removeStages(ctx, lease, digest, ids)
	}
	if m.removeErr != nil {
		return m.removeErr
	}
	state := m.states[digest]
	remove := make(map[int64]bool, len(ids))
	for _, id := range ids {
		remove[id] = true
	}
	kept := state.Stages[:0]
	for _, stage := range state.Stages {
		if !remove[stage.ID] {
			kept = append(kept, stage)
		}
	}
	state.Stages = kept
	m.states[digest] = state
	return nil
}

func (m *lifecycleMetadata) RemoveLocation(ctx context.Context, lease DigestLease, location Location) error {
	m.events = append(m.events, "remove-location:"+location.Key)
	if m.removeLocation != nil {
		return m.removeLocation(ctx, lease, location)
	}
	if m.removeErr != nil {
		return m.removeErr
	}
	state := m.states[location.Digest]
	kept := state.Locations[:0]
	for _, current := range state.Locations {
		if current != location {
			kept = append(kept, current)
		}
	}
	state.Locations = kept
	m.states[location.Digest] = state
	return nil
}

func (m *lifecycleMetadata) AddLocation(ctx context.Context, lease DigestLease, location Location) error {
	m.events = append(m.events, "add-location:"+location.Key)
	if m.addLocation != nil {
		return m.addLocation(ctx, lease, location)
	}
	state := m.states[location.Digest]
	state.Locations = append(state.Locations, location)
	m.states[location.Digest] = state
	return nil
}

func (m *lifecycleMetadata) MarkDigestExpired(ctx context.Context, lease DigestLease, digest Digest, now time.Time) (bool, error) {
	m.events = append(m.events, "mark-expired")
	if m.markDigestExpired != nil {
		return m.markDigestExpired(ctx, lease, digest, now)
	}
	return m.markResult, m.markErr
}

// ReapExpiredRetentionClaims records the cutoff the collect sweep computes.
// It intentionally does not append to m.events: content and metadata share
// that slice in the exact-order Collect tests, and the reap runs last on every
// Collect, so recording it there would perturb those unrelated assertions.
// reapCutoffs is what the reap-focused tests inspect to prove the sweep ran.
func (m *lifecycleMetadata) ReapExpiredRetentionClaims(_ context.Context, expiredBefore time.Time) (int, error) {
	m.reapCutoffs = append(m.reapCutoffs, expiredBefore)
	return m.reapCount, m.reapErr
}

type lifecycleContent struct {
	ContentStore
	events         *[]string
	deleteAll      func(context.Context, Digest) error
	deleteLocation func(context.Context, Location) error
}

func (c *lifecycleContent) DeleteAll(ctx context.Context, digest Digest) error {
	if c.events != nil {
		*c.events = append(*c.events, "delete-all")
	}
	if c.deleteAll != nil {
		return c.deleteAll(ctx, digest)
	}
	return nil
}

func (c *lifecycleContent) DeleteLocation(ctx context.Context, location Location) error {
	if c.events != nil {
		*c.events = append(*c.events, "delete-location:"+location.Key)
	}
	if c.deleteLocation != nil {
		return c.deleteLocation(ctx, location)
	}
	return nil
}

func (c *lifecycleContent) Put(context.Context, Digest, io.Reader) ([]Location, error) {
	return nil, nil
}

type lifecycleRepairer struct {
	result ReplicaRepairResult
	err    error
	repair func(context.Context, Snapshot, []Location) (ReplicaRepairResult, error)
	calls  []lifecycleRepairCall
}

type lifecycleRepairCall struct {
	snapshot  Snapshot
	locations []Location
}

func (r *lifecycleRepairer) RepairReplicas(ctx context.Context, snapshot Snapshot, locations []Location) (ReplicaRepairResult, error) {
	r.calls = append(r.calls, lifecycleRepairCall{snapshot: snapshot, locations: append([]Location(nil), locations...)})
	if r.repair != nil {
		return r.repair(ctx, snapshot, locations)
	}
	return r.result, r.err
}

type lifecycleLease struct {
	digests map[Digest]bool
	close   func() error
}

func (l *lifecycleLease) Covers(d Digest) bool { return l.digests[d] }
func (l *lifecycleLease) Close() error {
	if l.close != nil {
		return l.close()
	}
	return nil
}

type lifecycleLocks struct {
	acquire func(context.Context, []Digest) (DigestLease, error)
	calls   [][]Digest
}

type lifecycleBarrierLocks struct {
	token    chan struct{}
	attempts chan struct{}
}

func newLifecycleBarrierLocks() *lifecycleBarrierLocks {
	locks := &lifecycleBarrierLocks{token: make(chan struct{}, 1), attempts: make(chan struct{}, 8)}
	locks.token <- struct{}{}
	return locks
}

func (l *lifecycleBarrierLocks) AcquireMany(ctx context.Context, digests []Digest) (DigestLease, error) {
	l.attempts <- struct{}{}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.token:
	}
	covered := make(map[Digest]bool, len(digests))
	for _, digest := range digests {
		covered[digest] = true
	}
	var once sync.Once
	return &lifecycleLease{digests: covered, close: func() error {
		once.Do(func() { l.token <- struct{}{} })
		return nil
	}}, nil
}

func (l *lifecycleLocks) AcquireMany(ctx context.Context, digests []Digest) (DigestLease, error) {
	l.calls = append(l.calls, append([]Digest(nil), digests...))
	if l.acquire != nil {
		return l.acquire(ctx, digests)
	}
	covered := make(map[Digest]bool, len(digests))
	for _, digest := range digests {
		covered[digest] = true
	}
	return &lifecycleLease{digests: covered}, nil
}

func TestNewLifecycleRejectsMissingDependenciesAndInvalidPageSize(t *testing.T) {
	metadata := &lifecycleMetadata{}
	events := []string{}
	content := &lifecycleContent{events: &events}
	repairer := &lifecycleRepairer{}
	locks := &lifecycleLocks{}
	var typedNilMetadata *lifecycleMetadata
	var typedNilContent *lifecycleContent
	var typedNilRepairer *lifecycleRepairer
	var typedNilLocks *lifecycleLocks
	tests := []func() error{
		func() error { _, err := NewLifecycle(nil, content, repairer, locks); return err },
		func() error { _, err := NewLifecycle(typedNilMetadata, content, repairer, locks); return err },
		func() error { _, err := NewLifecycle(metadata, nil, repairer, locks); return err },
		func() error { _, err := NewLifecycle(metadata, typedNilContent, repairer, locks); return err },
		func() error { _, err := NewLifecycle(metadata, content, nil, locks); return err },
		func() error { _, err := NewLifecycle(metadata, content, typedNilRepairer, locks); return err },
		func() error { _, err := NewLifecycle(metadata, content, repairer, nil); return err },
		func() error { _, err := NewLifecycle(metadata, content, repairer, typedNilLocks); return err },
		func() error {
			_, err := NewLifecycle(metadata, content, repairer, locks, WithLifecyclePageSize(0))
			return err
		},
		func() error {
			_, err := NewLifecycle(metadata, content, repairer, locks, WithLifecyclePageSize(MaxLifecyclePageSize+1))
			return err
		},
		func() error {
			_, err := NewLifecycle(metadata, content, repairer, locks, WithLifecycleClock(nil))
			return err
		},
		func() error { _, err := NewLifecycle(metadata, content, repairer, locks, nil); return err },
	}
	for i, run := range tests {
		if err := run(); err == nil {
			t.Fatalf("invalid constructor case %d succeeded", i)
		}
	}
}

func TestLifecycleUsesOneBoundedPageWithIndependentCursorsAndRescansAfterTerminal(t *testing.T) {
	now := lifecycleNow()
	firstDigest := lifecycleDigest(t, '1')
	secondDigest := lifecycleDigest(t, '2')
	first := LifecycleCandidate{Digest: firstDigest, Kind: LifecycleCandidateOrphan}
	second := LifecycleCandidate{Digest: secondDigest, Kind: LifecycleCandidateExpiry}
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{
		firstDigest:  {Digest: firstDigest},
		secondDigest: {Digest: secondDigest, Snapshots: []Snapshot{lifecycleManifest(secondDigest, ContentStateExpired)}},
	}}
	metadata.discover = func(_ context.Context, request LifecyclePageRequest) (LifecycleCandidatePage, error) {
		switch request.After {
		case "":
			return LifecycleCandidatePage{Candidates: []LifecycleCandidate{first}, Next: first.Cursor()}, nil
		case first.Cursor():
			return LifecycleCandidatePage{Candidates: []LifecycleCandidate{second}}, nil
		default:
			t.Fatalf("unexpected cursor %q", request.After)
			return LifecycleCandidatePage{}, nil
		}
	}
	lifecycle, err := NewLifecycle(
		metadata,
		&lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{},
		&lifecycleLocks{},
		WithLifecycleClock(func() time.Time { return now }),
		WithLifecyclePageSize(1),
	)
	if err != nil {
		t.Fatal(err)
	}

	if report, err := lifecycle.Collect(context.Background()); err != nil || report.Scanned != 1 {
		t.Fatalf("first Collect() = %#v, %v", report, err)
	}
	if report, err := lifecycle.Collect(context.Background()); err != nil || report.Scanned != 1 {
		t.Fatalf("terminal Collect() = %#v, %v", report, err)
	}
	if report, err := lifecycle.Repair(context.Background()); err != nil || report.Scanned != 1 {
		t.Fatalf("independent Repair() = %#v, %v", report, err)
	}
	if report, err := lifecycle.Collect(context.Background()); err != nil || report.Scanned != 1 {
		t.Fatalf("reset Collect() = %#v, %v", report, err)
	}

	wantAfter := []LifecycleCursor{"", first.Cursor(), "", ""}
	if len(metadata.discoveryRequests) != len(wantAfter) {
		t.Fatalf("discovery requests = %#v", metadata.discoveryRequests)
	}
	for i, request := range metadata.discoveryRequests {
		if request.After != wantAfter[i] || request.Limit != 1 {
			t.Fatalf("discovery request %d = %#v, want after=%q limit=1", i, request, wantAfter[i])
		}
	}

	restarted, err := NewLifecycle(metadata, &lifecycleContent{}, &lifecycleRepairer{}, &lifecycleLocks{}, WithLifecyclePageSize(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := metadata.discoveryRequests[len(metadata.discoveryRequests)-1].After; got != "" {
		t.Fatalf("restart began after %q, want beginning", got)
	}
}

func TestLifecycleAdvancesPastPoisonAndSkippedCandidates(t *testing.T) {
	now := lifecycleNow()
	poisonDigest := lifecycleDigest(t, '1')
	skippedDigest := lifecycleDigest(t, '2')
	goodDigest := lifecycleDigest(t, '3')
	poison := errors.New("poison recheck")
	candidates := []LifecycleCandidate{
		{Digest: poisonDigest, Kind: LifecycleCandidateOrphan},
		{Digest: skippedDigest, Kind: LifecycleCandidateRepair},
		{Digest: goodDigest, Kind: LifecycleCandidateOrphan},
	}
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: candidates, Next: candidates[len(candidates)-1].Cursor()},
		states: map[Digest]DigestState{goodDigest: {
			Digest: goodDigest,
			Stages: []StagedUpload{lifecycleStage(1, goodDigest, now)},
		}},
		digestState: func(_ context.Context, _ DigestLease, digest Digest, _ time.Time) (DigestState, error) {
			if digest == poisonDigest {
				return DigestState{}, poison
			}
			return DigestState{Digest: goodDigest, Stages: []StagedUpload{lifecycleStage(1, goodDigest, now)}}, nil
		},
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if !errors.Is(err, poison) {
		t.Fatalf("Collect() error = %v, want poison", err)
	}
	var candidateErr LifecycleCandidateError
	if !errors.As(err, &candidateErr) || candidateErr.Candidate != candidates[0] || candidateErr.Phase != LifecyclePhaseRecheck {
		t.Fatalf("Collect() error = %v, want poison candidate context", err)
	}
	if report.Scanned != 3 || report.Failed != 1 || report.Deferred != 1 || report.StagesRemoved != 1 {
		t.Fatalf("report = %#v", report)
	}

	metadata.page = LifecycleCandidatePage{}
	if _, err := lifecycle.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := metadata.discoveryRequests[1].After; got != candidates[2].Cursor() {
		t.Fatalf("next page began after %q, want %q", got, candidates[2].Cursor())
	}
}

func TestLifecycleSkippedCandidateDoesNotAcquireDigestLease(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '2')
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
	}
	unexpectedLock := errors.New("skipped candidate tried to lock")
	locks := &lifecycleLocks{acquire: func(context.Context, []Digest) (DigestLease, error) {
		return nil, unexpectedLock
	}}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, locks, now)
	report, err := lifecycle.Collect(context.Background())
	if err != nil || report.Scanned != 1 || report.Deferred != 1 || report.Failed != 0 {
		t.Fatalf("Collect() = %#v, %v", report, err)
	}
	if len(locks.calls) != 0 {
		t.Fatalf("skipped candidate acquired %d digest leases", len(locks.calls))
	}
}

func TestLifecycleCancellationStopsBeforeNextCandidateAndResumesAfterAttemptedCandidate(t *testing.T) {
	now := lifecycleNow()
	firstDigest := lifecycleDigest(t, '1')
	secondDigest := lifecycleDigest(t, '2')
	first := LifecycleCandidate{Digest: firstDigest, Kind: LifecycleCandidateOrphan}
	second := LifecycleCandidate{Digest: secondDigest, Kind: LifecycleCandidateOrphan}
	ctx, cancel := context.WithCancel(context.Background())
	metadata := &lifecycleMetadata{states: map[Digest]DigestState{secondDigest: {Digest: secondDigest}}}
	metadata.discover = func(_ context.Context, request LifecyclePageRequest) (LifecycleCandidatePage, error) {
		if request.After == "" {
			return LifecycleCandidatePage{Candidates: []LifecycleCandidate{first, second}}, nil
		}
		if request.After == first.Cursor() {
			return LifecycleCandidatePage{Candidates: []LifecycleCandidate{second}}, nil
		}
		t.Fatalf("unexpected cursor %q", request.After)
		return LifecycleCandidatePage{}, nil
	}
	metadata.digestState = func(ctx context.Context, _ DigestLease, digest Digest, _ time.Time) (DigestState, error) {
		if digest == firstDigest {
			cancel()
			return DigestState{}, ctx.Err()
		}
		return DigestState{Digest: digest}, nil
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, &lifecycleLocks{}, now)

	report, err := lifecycle.Collect(ctx)
	if !errors.Is(err, context.Canceled) || report.Scanned != 1 || report.Failed != 0 {
		t.Fatalf("canceled Collect() = %#v, %v", report, err)
	}
	if metadata.digestStateCalls[secondDigest] != 0 {
		t.Fatal("second candidate was processed after cancellation")
	}
	if _, err := lifecycle.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := metadata.discoveryRequests[1].After; got != first.Cursor() {
		t.Fatalf("resume cursor = %q, want %q", got, first.Cursor())
	}
}

func TestLifecycleCancellationDuringPartialLockAcquisitionClosesLease(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '2')
	ctx, cancel := context.WithCancel(context.Background())
	closed := false
	locks := &lifecycleLocks{acquire: func(_ context.Context, digests []Digest) (DigestLease, error) {
		cancel()
		return &lifecycleLease{
			digests: map[Digest]bool{digests[0]: true},
			close: func() error {
				closed = true
				return nil
			},
		}, ctx.Err()
	}}
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, locks, now)
	report, err := lifecycle.Collect(ctx)
	if !errors.Is(err, context.Canceled) || report.Scanned != 1 || report.Failed != 0 || !closed {
		t.Fatalf("Collect() = %#v, %v, partial lease closed=%t", report, err, closed)
	}
}

func TestLifecycleJoinsLeaseCloseErrorAndContinuesLaterCandidates(t *testing.T) {
	now := lifecycleNow()
	firstDigest := lifecycleDigest(t, '1')
	secondDigest := lifecycleDigest(t, '2')
	closeFailure := errors.New("close lease")
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{
			{Digest: firstDigest, Kind: LifecycleCandidateOrphan},
			{Digest: secondDigest, Kind: LifecycleCandidateOrphan},
		}},
		states: map[Digest]DigestState{firstDigest: {Digest: firstDigest}, secondDigest: {Digest: secondDigest}},
	}
	acquires := 0
	locks := &lifecycleLocks{acquire: func(_ context.Context, digests []Digest) (DigestLease, error) {
		acquires++
		lease := &lifecycleLease{digests: map[Digest]bool{digests[0]: true}}
		if acquires == 1 {
			lease.close = func() error { return closeFailure }
		}
		return lease, nil
	}}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, locks, now)
	report, err := lifecycle.Collect(context.Background())
	if !errors.Is(err, closeFailure) {
		t.Fatalf("Collect() error = %v, want lease close failure", err)
	}
	if report.Scanned != 2 || report.Failed != 1 || report.Deferred != 1 || metadata.digestStateCalls[secondDigest] != 1 {
		t.Fatalf("report = %#v, calls = %#v", report, metadata.digestStateCalls)
	}
}

func TestLifecycleJoinsCandidateAndLeaseCloseFailures(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '3')
	recheckFailure := errors.New("recheck")
	closeFailure := errors.New("close")
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
		digestState: func(context.Context, DigestLease, Digest, time.Time) (DigestState, error) {
			return DigestState{}, recheckFailure
		},
	}
	locks := &lifecycleLocks{acquire: func(_ context.Context, digests []Digest) (DigestLease, error) {
		return &lifecycleLease{
			digests: map[Digest]bool{digests[0]: true},
			close:   func() error { return closeFailure },
		}, nil
	}}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, &lifecycleRepairer{}, locks, now)
	report, err := lifecycle.Collect(context.Background())
	if !errors.Is(err, recheckFailure) || !errors.Is(err, closeFailure) || report.Failed != 1 {
		t.Fatalf("Collect() = %#v, %v", report, err)
	}
}

func TestLifecycleDigestLeaseBlocksRepairUntilOrphanCleanupUnlocks(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '3')
	locks := newLifecycleBarrierLocks()
	deleteStarted := make(chan struct{})
	allowDelete := make(chan struct{})
	repairCalled := make(chan struct{})

	collectMetadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
		states: map[Digest]DigestState{digest: {
			Digest: digest, Stages: []StagedUpload{lifecycleStage(1, digest, now)},
		}},
	}
	collect := mustLifecycle(t, collectMetadata, &lifecycleContent{deleteAll: func(context.Context, Digest) error {
		close(deleteStarted)
		<-allowDelete
		return nil
	}}, &lifecycleRepairer{}, locks, now)

	repairMetadata := &lifecycleMetadata{
		page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states: map[Digest]DigestState{digest: lifecycleAvailableState(digest, true)},
	}
	repairer := &lifecycleRepairer{repair: func(context.Context, Snapshot, []Location) (ReplicaRepairResult, error) {
		close(repairCalled)
		return ReplicaRepairResult{Verified: 1, Desired: 1, LiveCapacity: 1}, nil
	}}
	repair := mustLifecycle(t, repairMetadata, &lifecycleContent{}, repairer, locks, now)

	collectDone := make(chan error, 1)
	go func() {
		_, err := collect.Collect(context.Background())
		collectDone <- err
	}()
	waitLifecycleSignal(t, locks.attempts, "collect lock attempt")
	waitLifecycleSignal(t, deleteStarted, "orphan delete")

	repairDone := make(chan error, 1)
	go func() {
		_, err := repair.Repair(context.Background())
		repairDone <- err
	}()
	waitLifecycleSignal(t, locks.attempts, "repair lock attempt")
	select {
	case <-repairCalled:
		t.Fatal("repair crossed the digest barrier during orphan cleanup")
	default:
	}

	close(allowDelete)
	if err := waitLifecycleResult(t, collectDone, "collect completion"); err != nil {
		t.Fatal(err)
	}
	if err := waitLifecycleResult(t, repairDone, "repair completion"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-repairCalled:
	default:
		t.Fatal("repair did not run after orphan cleanup unlocked")
	}
}

func TestLifecycleDigestLeaseBlocksRepairAcrossExpiryFinalCAS(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '3')
	locks := newLifecycleBarrierLocks()
	casStarted := make(chan struct{})
	allowCAS := make(chan struct{})
	repairCalled := make(chan struct{})

	collectMetadata := &lifecycleMetadata{
		page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
		states: map[Digest]DigestState{digest: lifecycleAvailableState(digest, false)},
		markDigestExpired: func(context.Context, DigestLease, Digest, time.Time) (bool, error) {
			close(casStarted)
			<-allowCAS
			return true, nil
		},
	}
	collect := mustLifecycle(t, collectMetadata, &lifecycleContent{}, &lifecycleRepairer{}, locks, now)
	repairMetadata := &lifecycleMetadata{
		page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states: map[Digest]DigestState{digest: lifecycleAvailableState(digest, true)},
	}
	repairer := &lifecycleRepairer{repair: func(context.Context, Snapshot, []Location) (ReplicaRepairResult, error) {
		close(repairCalled)
		return ReplicaRepairResult{Verified: 1, Desired: 1, LiveCapacity: 1}, nil
	}}
	repair := mustLifecycle(t, repairMetadata, &lifecycleContent{}, repairer, locks, now)

	collectDone := make(chan error, 1)
	go func() {
		_, err := collect.Collect(context.Background())
		collectDone <- err
	}()
	waitLifecycleSignal(t, locks.attempts, "expiry lock attempt")
	waitLifecycleSignal(t, casStarted, "expiry final CAS")

	repairDone := make(chan error, 1)
	go func() {
		_, err := repair.Repair(context.Background())
		repairDone <- err
	}()
	waitLifecycleSignal(t, locks.attempts, "repair lock attempt")
	select {
	case <-repairCalled:
		t.Fatal("repair crossed the digest barrier during expiry final CAS")
	default:
	}

	close(allowCAS)
	if err := waitLifecycleResult(t, collectDone, "expiry completion"); err != nil {
		t.Fatal(err)
	}
	if err := waitLifecycleResult(t, repairDone, "repair completion"); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycleCollectDeletesOrphanBytesBeforeStages(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	digest := mustTestDigest(t)
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
		states: map[Digest]DigestState{digest: {
			Digest: digest,
			Stages: []StagedUpload{{ID: 1, Digest: digest, TeamID: 1, Attempt: "upload:k", LeaseExpiresAt: now, CreatedAt: now.Add(-time.Hour)}},
		}},
	}
	content := &lifecycleContent{events: &metadata.events}
	lifecycle, err := NewLifecycle(metadata, content, &lifecycleRepairer{}, &lifecycleLocks{}, WithLifecycleClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	report, err := lifecycle.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.StagesRemoved != 1 || report.LocationsDeleted != 0 || report.Scanned != 1 {
		t.Fatalf("report = %#v", report)
	}
	want := []string{"delete-all", "remove-stages"}
	if len(metadata.events) != len(want) || metadata.events[0] != want[0] || metadata.events[1] != want[1] {
		t.Fatalf("events = %v, want %v", metadata.events, want)
	}
}

func TestLifecycleCollectManifestOrphanRemovesOnlyExpiredStagesDespiteUnexpiredSibling(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '4')
	removed := []int64(nil)
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
		states: map[Digest]DigestState{digest: {
			Digest:    digest,
			Snapshots: []Snapshot{lifecycleManifest(digest, ContentStateAvailable)},
			Stages: []StagedUpload{
				lifecycleStage(1, digest, now),
				lifecycleStage(2, digest, now.Add(time.Nanosecond)),
			},
		}},
		removeStages: func(_ context.Context, _ DigestLease, _ Digest, ids []int64) error {
			removed = append([]int64(nil), ids...)
			return nil
		},
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.StagesRemoved != 1 || report.Deferred != 0 {
		t.Fatalf("report = %#v", report)
	}
	if len(removed) != 1 || removed[0] != 1 {
		t.Fatalf("removed stages = %v, want [1]", removed)
	}
	if len(metadata.events) != 1 || metadata.events[0] != "remove-stages" {
		t.Fatalf("manifest orphan events = %v, want only stage cleanup", metadata.events)
	}
}

func TestLifecycleCollectOrphanWithoutManifestDefersAllCleanupForAnyUnexpiredSibling(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '4')
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
		states: map[Digest]DigestState{digest: {
			Digest: digest,
			Stages: []StagedUpload{
				lifecycleStage(1, digest, now),
				lifecycleStage(2, digest, now.Add(time.Nanosecond)),
			},
		}},
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Deferred != 1 || report.StagesRemoved != 0 || len(metadata.events) != 0 {
		t.Fatalf("report = %#v, events = %v", report, metadata.events)
	}
}

func TestLifecycleCollectOrphanRetriesDeleteAndStageCleanupFailures(t *testing.T) {
	now := lifecycleNow()
	for _, test := range []struct {
		name      string
		failPhase LifecyclePhase
	}{
		{name: "physical delete", failPhase: LifecyclePhaseDelete},
		{name: "stage row cleanup", failPhase: LifecyclePhaseStageCleanup},
	} {
		t.Run(test.name, func(t *testing.T) {
			digest := lifecycleDigest(t, '5')
			failure := errors.New(test.name)
			metadata := &lifecycleMetadata{
				page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
				states: map[Digest]DigestState{digest: {Digest: digest, Stages: []StagedUpload{lifecycleStage(1, digest, now)}}},
			}
			deleteCalls := 0
			content := &lifecycleContent{events: &metadata.events, deleteAll: func(context.Context, Digest) error {
				deleteCalls++
				if test.failPhase == LifecyclePhaseDelete && deleteCalls == 1 {
					return failure
				}
				return nil
			}}
			removeCalls := 0
			metadata.removeStages = func(_ context.Context, _ DigestLease, _ Digest, _ []int64) error {
				removeCalls++
				if test.failPhase == LifecyclePhaseStageCleanup && removeCalls == 1 {
					return failure
				}
				return nil
			}
			lifecycle := mustLifecycle(t, metadata, content, &lifecycleRepairer{}, &lifecycleLocks{}, now)

			first, err := lifecycle.Collect(context.Background())
			if !errors.Is(err, failure) || first.Failed != 1 || first.StagesRemoved != 0 {
				t.Fatalf("first Collect() = %#v, %v", first, err)
			}
			var candidateErr LifecycleCandidateError
			if !errors.As(err, &candidateErr) || candidateErr.Phase != test.failPhase {
				t.Fatalf("first error = %v, want phase %s", err, test.failPhase)
			}
			second, err := lifecycle.Collect(context.Background())
			if err != nil || second.StagesRemoved != 1 {
				t.Fatalf("retry Collect() = %#v, %v", second, err)
			}
			if deleteCalls != 2 {
				t.Fatalf("delete calls = %d, want safe retry", deleteCalls)
			}
		})
	}
}

func TestLifecycleCollectExpiresOnlyAfterLocationsAndFinalSweep(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	digest := mustTestDigest(t)
	location := Location{Digest: digest, Driver: "jetbridge", Key: "canonical", Node: "node-a"}
	metadata := &lifecycleMetadata{
		page:       LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
		states:     map[Digest]DigestState{digest: {Digest: digest, Snapshots: []Snapshot{lifecycleManifest(digest, ContentStateAvailable)}, Locations: []Location{location}}},
		markResult: true,
	}
	content := &lifecycleContent{events: &metadata.events}
	lifecycle, err := NewLifecycle(metadata, content, &lifecycleRepairer{}, &lifecycleLocks{}, WithLifecycleClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	report, err := lifecycle.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.DigestsExpired != 1 || report.LocationsDeleted != 1 {
		t.Fatalf("report = %#v", report)
	}
	want := []string{"delete-location:canonical", "remove-location:canonical", "delete-all", "mark-expired"}
	if len(metadata.events) != len(want) {
		t.Fatalf("events = %v, want %v", metadata.events, want)
	}
	for i := range want {
		if metadata.events[i] != want[i] {
			t.Fatalf("events = %v, want %v", metadata.events, want)
		}
	}
}

func TestLifecycleCollectExpiryDefersForEveryRetentionAndProtectedState(t *testing.T) {
	now := lifecycleNow()
	for _, test := range []struct {
		name  string
		state func(Digest) DigestState
	}{
		{name: "future binding", state: func(d Digest) DigestState { return lifecycleAvailableState(d, true) }},
		{name: "workflow", state: func(d Digest) DigestState { return lifecycleAvailableState(d, true) }},
		{name: "fixture", state: func(d Digest) DigestState { return lifecycleAvailableState(d, true) }},
		{name: "Alice pin", state: func(d Digest) DigestState { return lifecycleAvailableState(d, true) }},
		{name: "Bob pin", state: func(d Digest) DigestState { return lifecycleAvailableState(d, true) }},
		{name: "unexpired stage", state: func(d Digest) DigestState {
			state := lifecycleAvailableState(d, false)
			state.Stages = []StagedUpload{lifecycleStage(1, d, now.Add(time.Nanosecond))}
			return state
		}},
		{name: "already expired manifest", state: func(d Digest) DigestState {
			return DigestState{Digest: d, Snapshots: []Snapshot{lifecycleManifest(d, ContentStateExpired)}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			digest := lifecycleDigest(t, '6')
			metadata := &lifecycleMetadata{
				page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
				states: map[Digest]DigestState{digest: test.state(digest)},
			}
			lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
			report, err := lifecycle.Collect(context.Background())
			if err != nil || report.Deferred != 1 || report.DigestsExpired != 0 || len(metadata.events) != 0 {
				t.Fatalf("Collect() = %#v, %v, events %v", report, err, metadata.events)
			}
		})
	}
}

func TestLifecycleCollectExpiryTreatsStageLeaseEqualityAsExpired(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '6')
	state := lifecycleAvailableState(digest, false)
	state.Stages = []StagedUpload{lifecycleStage(1, digest, now)}
	metadata := &lifecycleMetadata{
		page:       LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
		states:     map[Digest]DigestState{digest: state},
		markResult: true,
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err != nil || report.DigestsExpired != 1 {
		t.Fatalf("Collect() = %#v, %v", report, err)
	}
}

func TestLifecycleCollectExpiryDeletesLocationsInStableOrder(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '6')
	locations := []Location{
		{Digest: digest, Driver: "z", Key: "a", Node: "a"},
		{Digest: digest, Driver: "a", Key: "z", Node: "b"},
		{Digest: digest, Driver: "a", Key: "z", Node: "a"},
	}
	state := lifecycleAvailableState(digest, false)
	state.Locations = locations
	metadata := &lifecycleMetadata{
		page:       LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
		states:     map[Digest]DigestState{digest: state},
		markResult: true,
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
	if _, err := lifecycle.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"delete-location:z", "remove-location:z",
		"delete-location:z", "remove-location:z",
		"delete-location:a", "remove-location:a",
		"delete-all", "mark-expired",
	}
	if len(metadata.events) != len(want) {
		t.Fatalf("events = %v, want %v", metadata.events, want)
	}
	for i := range want {
		if metadata.events[i] != want[i] {
			t.Fatalf("events = %v, want %v", metadata.events, want)
		}
	}
}

func TestLifecycleCollectExpiryRetriesEveryFailureBoundary(t *testing.T) {
	now := lifecycleNow()
	for _, test := range []struct {
		name  string
		phase LifecyclePhase
	}{
		{name: "delete location", phase: LifecyclePhaseDelete},
		{name: "remove location metadata", phase: LifecyclePhaseMetadata},
		{name: "final recheck", phase: LifecyclePhaseRecheck},
		{name: "final broadcast", phase: LifecyclePhaseDelete},
		{name: "expiry transaction", phase: LifecyclePhaseExpire},
		{name: "false expiry compare and set", phase: LifecyclePhaseExpire},
	} {
		t.Run(test.name, func(t *testing.T) {
			digest := lifecycleDigest(t, '7')
			location := Location{Digest: digest, Driver: "jetbridge", Key: "canonical", Node: "node-a"}
			state := lifecycleAvailableState(digest, false)
			state.Locations = []Location{location}
			failure := errors.New(test.name)
			metadata := &lifecycleMetadata{
				page:       LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
				states:     map[Digest]DigestState{digest: state},
				markResult: true,
			}
			content := &lifecycleContent{events: &metadata.events}
			failed := false
			switch test.name {
			case "delete location":
				content.deleteLocation = func(context.Context, Location) error {
					if !failed {
						failed = true
						return failure
					}
					return nil
				}
			case "remove location metadata":
				metadata.removeLocation = func(_ context.Context, _ DigestLease, got Location) error {
					if !failed {
						failed = true
						return failure
					}
					removeLifecycleLocation(metadata, got)
					return nil
				}
			case "final recheck":
				metadata.digestState = func(_ context.Context, _ DigestLease, got Digest, _ time.Time) (DigestState, error) {
					if metadata.digestStateCalls[got] == 2 && !failed {
						failed = true
						return DigestState{}, failure
					}
					return metadata.states[got].Clone(), nil
				}
			case "final broadcast":
				content.deleteAll = func(context.Context, Digest) error {
					if !failed {
						failed = true
						return failure
					}
					return nil
				}
			case "expiry transaction":
				metadata.markDigestExpired = func(context.Context, DigestLease, Digest, time.Time) (bool, error) {
					if !failed {
						failed = true
						return false, failure
					}
					return true, nil
				}
			case "false expiry compare and set":
				metadata.markDigestExpired = func(context.Context, DigestLease, Digest, time.Time) (bool, error) {
					if !failed {
						failed = true
						return false, nil
					}
					return true, nil
				}
			}
			lifecycle := mustLifecycle(t, metadata, content, &lifecycleRepairer{}, &lifecycleLocks{}, now)

			first, err := lifecycle.Collect(context.Background())
			if err == nil || first.Failed != 1 || first.DigestsExpired != 0 {
				t.Fatalf("first Collect() = %#v, %v", first, err)
			}
			if test.name == "false expiry compare and set" {
				if !errors.Is(err, ErrLifecycleStateChange) {
					t.Fatalf("first error = %v, want state change", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("first error = %v, want %v", err, failure)
			}
			var candidateErr LifecycleCandidateError
			if !errors.As(err, &candidateErr) || candidateErr.Phase != test.phase {
				t.Fatalf("first error = %v, want phase %s", err, test.phase)
			}
			second, err := lifecycle.Collect(context.Background())
			if err != nil || second.DigestsExpired != 1 {
				t.Fatalf("retry Collect() = %#v, %v", second, err)
			}
		})
	}
}

func TestLifecycleCollectExpiryRejectsStateChangeBeforeFinalSweep(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '7')
	location := Location{Digest: digest, Driver: "jetbridge", Key: "canonical", Node: "node-a"}
	state := lifecycleAvailableState(digest, false)
	state.Locations = []Location{location}
	metadata := &lifecycleMetadata{
		page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateExpiry}}},
		states: map[Digest]DigestState{digest: state},
	}
	metadata.digestState = func(_ context.Context, _ DigestLease, got Digest, _ time.Time) (DigestState, error) {
		state := metadata.states[got].Clone()
		if metadata.digestStateCalls[got] == 2 {
			state.HasActiveRetention = true
		}
		return state, nil
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events}, &lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if !errors.Is(err, ErrLifecycleStateChange) || report.Failed != 1 || report.DigestsExpired != 0 {
		t.Fatalf("Collect() = %#v, %v", report, err)
	}
	for _, event := range metadata.events {
		if event == "delete-all" || event == "mark-expired" {
			t.Fatalf("state change still reached destructive finalization: %v", metadata.events)
		}
	}
}

func TestLifecycleRepairAppliesPartialAdditionsBeforePruningAndReturnsCandidateError(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	digest := mustTestDigest(t)
	oldLocation := Location{Digest: digest, Driver: "jetbridge", Key: "old", Node: "node-a"}
	newLocation := Location{Digest: digest, Driver: "jetbridge", Key: "new", Node: "node-b"}
	claim := RetentionClaim{ID: 1, SnapshotID: 1, Class: RetentionClassWorkflow, Reason: "workflow", CreatedAt: now.Add(-time.Hour)}
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states: map[Digest]DigestState{digest: {
			Digest: digest, Snapshots: []Snapshot{lifecycleManifest(digest, ContentStateAvailable)},
			Locations: []Location{oldLocation}, HasActiveRetention: claim.Active(now),
		}},
	}
	repairFailure := errors.New("second replica unavailable")
	repairer := &lifecycleRepairer{result: ReplicaRepairResult{
		Added: []Location{newLocation}, Removed: []Location{oldLocation}, Verified: 1, Desired: 2, LiveCapacity: 2,
	}, err: repairFailure}
	content := &lifecycleContent{events: &metadata.events}
	lifecycle, err := NewLifecycle(metadata, content, repairer, &lifecycleLocks{}, WithLifecycleClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	report, err := lifecycle.Repair(context.Background())
	if err == nil || !errors.Is(err, repairFailure) {
		t.Fatalf("Repair() error = %v", err)
	}
	var candidateErr LifecycleCandidateError
	if !errors.As(err, &candidateErr) || candidateErr.Phase != LifecyclePhaseCopy {
		t.Fatalf("Repair() error = %v, want copy-phase candidate error", err)
	}
	if report.LocationsAdded != 1 || report.StalePruned != 1 || report.Failed != 1 {
		t.Fatalf("report = %#v", report)
	}
	want := []string{"add-location:new", "remove-location:old"}
	if len(metadata.events) != len(want) || metadata.events[0] != want[0] || metadata.events[1] != want[1] {
		t.Fatalf("events = %v, want %v", metadata.events, want)
	}
}

func TestLifecycleRepairPreservesOfflineLocationSoLaterExpiryRemainsRetryable(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '7')
	online := Location{Digest: digest, Driver: "jetbridge", Key: "a-online", Node: "node-a"}
	offline := Location{Digest: digest, Driver: "jetbridge", Key: "z-offline", Node: "node-offline"}
	state := lifecycleAvailableState(digest, true)
	state.Locations = []Location{offline, online}
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{
			{Digest: digest, Kind: LifecycleCandidateExpiry},
			{Digest: digest, Kind: LifecycleCandidateRepair},
		}},
		states:     map[Digest]DigestState{digest: state},
		markResult: true,
	}
	repairer := &lifecycleRepairer{result: ReplicaRepairResult{
		Verified: 1, Desired: 1, LiveCapacity: 1,
	}}
	offlineErr := errors.New("offline node is not addressable")
	content := &lifecycleContent{
		events: &metadata.events,
		deleteLocation: func(_ context.Context, location Location) error {
			if location == offline {
				return offlineErr
			}
			return nil
		},
	}
	lifecycle := mustLifecycle(t, metadata, content, repairer, &lifecycleLocks{}, now)

	repairReport, err := lifecycle.Repair(context.Background())
	if err != nil || repairReport.LocationsAdded != 0 || repairReport.StalePruned != 0 {
		t.Fatalf("Repair() = %#v, %v", repairReport, err)
	}
	if !equalLocations(metadata.states[digest].Locations, []Location{offline, online}) {
		t.Fatalf("repair discarded an unaddressable recorded location: %#v", metadata.states[digest].Locations)
	}

	state = metadata.states[digest]
	state.HasActiveRetention = false
	metadata.states[digest] = state
	expiryReport, err := lifecycle.Collect(context.Background())
	if !errors.Is(err, offlineErr) || expiryReport.Failed != 1 || expiryReport.DigestsExpired != 0 {
		t.Fatalf("Collect() = %#v, %v", expiryReport, err)
	}
	if !equalLocations(metadata.states[digest].Locations, []Location{offline}) {
		t.Fatalf("expiry did not preserve the offline row for retry: %#v", metadata.states[digest].Locations)
	}
	for _, event := range metadata.events {
		if event == "delete-all" || event == "mark-expired" {
			t.Fatalf("expiry finalized while an offline recorded row remained: %v", metadata.events)
		}
	}
}

func TestLifecycleRepairSkipsUnhealthyCandidateStatesWithoutCallingRepairer(t *testing.T) {
	now := lifecycleNow()
	for _, test := range []struct {
		name  string
		state func(Digest) DigestState
	}{
		{name: "unretained", state: func(d Digest) DigestState { return lifecycleAvailableState(d, false) }},
		{name: "expired", state: func(d Digest) DigestState {
			return DigestState{Digest: d, Snapshots: []Snapshot{lifecycleManifest(d, ContentStateExpired)}, HasActiveRetention: true}
		}},
		{name: "no manifest", state: func(d Digest) DigestState { return DigestState{Digest: d, HasActiveRetention: true} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			digest := lifecycleDigest(t, '8')
			metadata := &lifecycleMetadata{
				page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
				states: map[Digest]DigestState{digest: test.state(digest)},
			}
			repairer := &lifecycleRepairer{}
			lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, repairer, &lifecycleLocks{}, now)
			report, err := lifecycle.Repair(context.Background())
			if err != nil || report.Deferred != 1 || len(repairer.calls) != 0 {
				t.Fatalf("Repair() = %#v, %v, calls %d", report, err, len(repairer.calls))
			}
		})
	}
}

func TestLifecycleRepairChoosesFirstSortedManifestAndSortsLocations(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '8')
	manifestZ := lifecycleManifest(digest, ContentStateAvailable)
	manifestZ.ID = 3
	manifestZ.Type = TypeRef("z/v1")
	manifestA2 := lifecycleManifest(digest, ContentStateAvailable)
	manifestA2.ID = 2
	manifestA2.Type = TypeRef("a/v1")
	manifestA1 := lifecycleManifest(digest, ContentStateAvailable)
	manifestA1.ID = 1
	manifestA1.Type = TypeRef("a/v1")
	locations := []Location{
		{Digest: digest, Driver: "z", Key: "a", Node: "a"},
		{Digest: digest, Driver: "a", Key: "z", Node: "b"},
		{Digest: digest, Driver: "a", Key: "z", Node: "a"},
	}
	metadata := &lifecycleMetadata{
		page: LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states: map[Digest]DigestState{digest: {
			Digest: digest, Snapshots: []Snapshot{manifestZ, manifestA2, manifestA1},
			Locations: locations, HasActiveRetention: true,
		}},
	}
	repairer := &lifecycleRepairer{result: ReplicaRepairResult{Verified: 1, Desired: 1, LiveCapacity: 1}}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, repairer, &lifecycleLocks{}, now)
	report, err := lifecycle.Repair(context.Background())
	if err != nil || report.Failed != 0 || report.Deferred != 0 {
		t.Fatalf("Repair() = %#v, %v", report, err)
	}
	if len(repairer.calls) != 1 || repairer.calls[0].snapshot.ID != 1 || repairer.calls[0].snapshot.Type != TypeRef("a/v1") {
		t.Fatalf("repair call = %#v", repairer.calls)
	}
	want := []Location{locations[2], locations[1], locations[0]}
	if !equalLocations(repairer.calls[0].locations, want) {
		t.Fatalf("repair locations = %#v, want %#v", repairer.calls[0].locations, want)
	}
}

func TestLifecycleRepairNoReadableReplicaPreservesAllMetadata(t *testing.T) {
	now := lifecycleNow()
	digest := lifecycleDigest(t, '8')
	location := Location{Digest: digest, Driver: "jetbridge", Key: "canonical", Node: "node-a"}
	state := lifecycleAvailableState(digest, true)
	state.Locations = []Location{location}
	metadata := &lifecycleMetadata{
		page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states: map[Digest]DigestState{digest: state},
	}
	repairer := &lifecycleRepairer{
		result: ReplicaRepairResult{Verified: 0, Desired: 1, LiveCapacity: 1},
		err:    ErrNoReadableReplica,
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, repairer, &lifecycleLocks{}, now)
	report, err := lifecycle.Repair(context.Background())
	if !errors.Is(err, ErrNoReadableReplica) || report.Failed != 1 || report.LocationsAdded != 0 || report.StalePruned != 0 {
		t.Fatalf("Repair() = %#v, %v", report, err)
	}
	var candidateErr LifecycleCandidateError
	if !errors.As(err, &candidateErr) || candidateErr.Phase != LifecyclePhaseVerify {
		t.Fatalf("Repair() error = %v, want verify phase", err)
	}
	if len(metadata.events) != 0 || !equalLocations(metadata.states[digest].Locations, []Location{location}) {
		t.Fatalf("metadata changed after no-readable result: events=%v state=%#v", metadata.events, metadata.states[digest])
	}
}

func TestLifecycleRepairRetriesMetadataFailuresWithoutPruningBeforeAdd(t *testing.T) {
	now := lifecycleNow()
	for _, failOperation := range []string{"add", "remove"} {
		t.Run(failOperation, func(t *testing.T) {
			digest := lifecycleDigest(t, '9')
			oldLocation := Location{Digest: digest, Driver: "jetbridge", Key: "old", Node: "node-a"}
			newLocation := Location{Digest: digest, Driver: "jetbridge", Key: "new", Node: "node-b"}
			state := lifecycleAvailableState(digest, true)
			state.Locations = []Location{oldLocation}
			failure := errors.New(failOperation + " metadata")
			metadata := &lifecycleMetadata{
				page:   LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
				states: map[Digest]DigestState{digest: state},
			}
			failed := false
			metadata.addLocation = func(_ context.Context, _ DigestLease, location Location) error {
				if failOperation == "add" && !failed {
					failed = true
					return failure
				}
				state := metadata.states[location.Digest]
				if !locationPresent(state.Locations, location) {
					state.Locations = append(state.Locations, location)
					metadata.states[location.Digest] = state
				}
				return nil
			}
			metadata.removeLocation = func(_ context.Context, _ DigestLease, location Location) error {
				if failOperation == "remove" && !failed {
					failed = true
					return failure
				}
				removeLifecycleLocation(metadata, location)
				return nil
			}
			repairer := &lifecycleRepairer{repair: func(_ context.Context, _ Snapshot, _ []Location) (ReplicaRepairResult, error) {
				result := ReplicaRepairResult{Removed: []Location{oldLocation}, Verified: 1, Desired: 1, LiveCapacity: 2}
				if !locationPresent(metadata.states[digest].Locations, newLocation) {
					result.Added = []Location{newLocation}
				}
				return result, nil
			}}
			lifecycle := mustLifecycle(t, metadata, &lifecycleContent{}, repairer, &lifecycleLocks{}, now)

			first, err := lifecycle.Repair(context.Background())
			if !errors.Is(err, failure) || first.Failed != 1 {
				t.Fatalf("first Repair() = %#v, %v", first, err)
			}
			var candidateErr LifecycleCandidateError
			if !errors.As(err, &candidateErr) || candidateErr.Phase != LifecyclePhaseMetadata {
				t.Fatalf("first error = %v, want metadata phase", err)
			}
			if failOperation == "add" && !locationPresent(metadata.states[digest].Locations, oldLocation) {
				t.Fatal("stale location was pruned after the replacement add failed")
			}
			second, err := lifecycle.Repair(context.Background())
			if err != nil || second.Failed != 0 {
				t.Fatalf("retry Repair() = %#v, %v", second, err)
			}
			if !locationPresent(metadata.states[digest].Locations, newLocation) || locationPresent(metadata.states[digest].Locations, oldLocation) {
				t.Fatalf("final locations = %#v", metadata.states[digest].Locations)
			}
		})
	}
}

func TestLifecycleCollectReapsClaimsBehindTheGracePeriod(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = 30 * 24 * time.Hour

	metadata := &lifecycleMetadata{reapCount: 7}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.ClaimsReaped != 7 {
		t.Fatalf("report = %#v", report)
	}
	if len(metadata.reapCutoffs) != 1 || !metadata.reapCutoffs[0].Equal(now.Add(-30*24*time.Hour)) {
		t.Fatalf("cutoffs = %v, want one at now-30d", metadata.reapCutoffs)
	}
}

func TestLifecycleCollectZeroGraceStillPassesTheUnshiftedClock(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = 0

	metadata := &lifecycleMetadata{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	if _, err := lifecycle.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The store deletes strictly before the cutoff, so a zero grace can still
	// never reach a claim that expires at or after this instant.
	if len(metadata.reapCutoffs) != 1 || !metadata.reapCutoffs[0].Equal(now) {
		t.Fatalf("cutoffs = %v, want exactly [%s]", metadata.reapCutoffs, now)
	}
}

func TestLifecycleCollectRefusesNegativeGraceAndReapsNothing(t *testing.T) {
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = -time.Second

	metadata := &lifecycleMetadata{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, lifecycleNow())
	report, err := lifecycle.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grace period must not be negative") {
		t.Fatalf("error = %v", err)
	}
	if len(metadata.reapCutoffs) != 0 || report.ClaimsReaped != 0 {
		t.Fatalf("a negative grace must never reach the store: %v", metadata.reapCutoffs)
	}
}

func TestLifecycleCollectJoinsClaimReapFailureWithCandidateProgress(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = time.Hour

	digest := lifecycleDigest(t, '4')
	metadata := &lifecycleMetadata{
		page:      LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateRepair}}},
		states:    map[Digest]DigestState{},
		reapErr:   errors.New("claim sweep failed"),
		reapCount: 3,
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	report, err := lifecycle.Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "claim sweep failed") {
		t.Fatalf("error = %v", err)
	}
	if report.Scanned != 1 || report.Deferred != 1 || report.ClaimsReaped != 0 {
		t.Fatalf("a failed sweep must not report reaped claims: %#v", report)
	}
}

func TestLifecycleCollectSkipsClaimReapAfterCancellation(t *testing.T) {
	now := lifecycleNow()
	defer restoreRetentionClaimGrace(retentionClaimGracePeriod)
	retentionClaimGracePeriod = time.Hour

	digest := lifecycleDigest(t, '5')
	metadata := &lifecycleMetadata{
		page:      LifecycleCandidatePage{Candidates: []LifecycleCandidate{{Digest: digest, Kind: LifecycleCandidateOrphan}}},
		states:    map[Digest]DigestState{digest: {Digest: digest}},
		reapCount: 5,
	}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := lifecycle.Collect(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() error = %v, want context.Canceled", err)
	}
	if len(metadata.reapCutoffs) != 0 {
		t.Fatalf("claim reap ran after cancellation: %v", metadata.reapCutoffs)
	}
	if report.ClaimsReaped != 0 {
		t.Fatalf("report.ClaimsReaped = %d after cancellation", report.ClaimsReaped)
	}
}

func TestLifecycleRepairNeverReapsClaims(t *testing.T) {
	metadata := &lifecycleMetadata{}
	lifecycle := mustLifecycle(t, metadata, &lifecycleContent{events: &metadata.events},
		&lifecycleRepairer{}, &lifecycleLocks{}, lifecycleNow())
	if _, err := lifecycle.Repair(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(metadata.reapCutoffs) != 0 {
		t.Fatalf("Repair reaped claims: %v", metadata.reapCutoffs)
	}
}

func restoreRetentionClaimGrace(previous time.Duration) {
	retentionClaimGracePeriod = previous
}

func lifecycleManifest(digest Digest, state ContentState) Snapshot {
	return Snapshot{
		ID: 1, Type: TypeRef("opaque/v1"), Digest: digest, ByteSize: 1, FileCount: 1,
		Representation: "application/x-tar", ContentState: state,
		CreatedAt: time.Date(2026, time.July, 21, 12, 0, 0, 0, time.UTC),
	}
}

func lifecycleNow() time.Time {
	return time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
}

func lifecycleDigest(t *testing.T, digit byte) Digest {
	t.Helper()
	digest, err := ParseDigest("sha256:" + strings.Repeat(string(digit), 64))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func lifecycleStage(id int64, digest Digest, expires time.Time) StagedUpload {
	return StagedUpload{
		ID: id, Digest: digest, TeamID: 1, Attempt: "upload:attempt",
		LeaseExpiresAt: expires, CreatedAt: lifecycleNow().Add(-time.Hour),
	}
}

func lifecycleAvailableState(digest Digest, retained bool) DigestState {
	return DigestState{
		Digest: digest, Snapshots: []Snapshot{lifecycleManifest(digest, ContentStateAvailable)},
		HasActiveRetention: retained,
	}
}

func removeLifecycleLocation(metadata *lifecycleMetadata, location Location) {
	state := metadata.states[location.Digest]
	kept := state.Locations[:0]
	for _, current := range state.Locations {
		if current != location {
			kept = append(kept, current)
		}
	}
	state.Locations = kept
	metadata.states[location.Digest] = state
}

func locationPresent(locations []Location, want Location) bool {
	for _, location := range locations {
		if location == want {
			return true
		}
	}
	return false
}

func equalLocations(got, want []Location) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func waitLifecycleSignal(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitLifecycleResult(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}

func mustLifecycle(
	t *testing.T,
	metadata MetadataStore,
	content ContentStore,
	repairer ReplicaRepairer,
	locks DigestLockManager,
	now time.Time,
	options ...LifecycleOption,
) *Lifecycle {
	t.Helper()
	options = append(options, WithLifecycleClock(func() time.Time { return now }))
	lifecycle, err := NewLifecycle(metadata, content, repairer, locks, options...)
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle
}
