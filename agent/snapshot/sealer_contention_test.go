package snapshot

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
	"time"
)

// blockingDigestLocks is a DigestLockManager that actually excludes.
//
// sealerLocks, the fake every other sealer test uses, returns the same lease to
// every caller and never blocks: two sealers can both be "holding" the same
// digest at once, which is adequate for sequencing assertions and useless for
// contention. This one models the PostgreSQL session advisory lock the real
// manager takes — one holder per digest, acquired in the caller's already-sorted
// order, released on Close — and records the maximum number of simultaneous
// holders, so exclusion is asserted rather than assumed.
type blockingDigestLocks struct {
	mu        sync.Mutex
	semaphore map[Digest]chan struct{}
	holding   map[Digest]int
	maxHeld   int
	acquires  int
}

func newBlockingDigestLocks() *blockingDigestLocks {
	return &blockingDigestLocks{
		semaphore: map[Digest]chan struct{}{},
		holding:   map[Digest]int{},
	}
}

func (l *blockingDigestLocks) gate(digest Digest) chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	gate, found := l.semaphore[digest]
	if !found {
		gate = make(chan struct{}, 1)
		l.semaphore[digest] = gate
	}
	return gate
}

func (l *blockingDigestLocks) enter(digest Digest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holding[digest]++
	if l.holding[digest] > l.maxHeld {
		l.maxHeld = l.holding[digest]
	}
}

func (l *blockingDigestLocks) leave(digest Digest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.holding[digest]--
}

func (l *blockingDigestLocks) AcquireMany(ctx context.Context, digests []Digest) (DigestLease, error) {
	l.mu.Lock()
	l.acquires++
	l.mu.Unlock()

	lease := &blockingDigestLease{manager: l, covered: map[Digest]bool{}}
	for _, digest := range digests {
		select {
		case <-ctx.Done():
			return lease, ctx.Err()
		case l.gate(digest) <- struct{}{}:
		}
		lease.acquired = append(lease.acquired, digest)
		lease.covered[digest] = true
		l.enter(digest)
	}
	return lease, nil
}

func (l *blockingDigestLocks) stats() (acquires int, maxHeld int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquires, l.maxHeld
}

type blockingDigestLease struct {
	manager   *blockingDigestLocks
	covered   map[Digest]bool
	acquired  []Digest
	closeOnce sync.Once
}

func (l *blockingDigestLease) Covers(digest Digest) bool { return l.covered[digest] }

func (l *blockingDigestLease) Close() error {
	l.closeOnce.Do(func() {
		for i := len(l.acquired) - 1; i >= 0; i-- {
			digest := l.acquired[i]
			l.manager.leave(digest)
			<-l.manager.gate(digest)
		}
	})
	return nil
}

// concurrentSealStore is a metadata store that behaves like the real one where
// convergence is decided: identity is keyed on the digest, so the second sealer
// to arrive finds the first's committed manifest and reuses it rather than
// creating a second value.
type concurrentSealStore struct {
	MetadataStore
	mu        sync.Mutex
	now       time.Time
	nextID    SnapshotID
	stages    int
	commits   int
	created   int
	snapshots map[Digest]Snapshot
	locations map[Digest][]Location
}

func newConcurrentSealStore(now time.Time) *concurrentSealStore {
	return &concurrentSealStore{
		now:       now,
		snapshots: map[Digest]Snapshot{},
		locations: map[Digest][]Location{},
	}
}

func (s *concurrentSealStore) StageUpload(_ context.Context, _ DigestLease, request StageUploadRequest) (StagedUpload, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stages++
	return StagedUpload{
		ID: int64(s.stages), Digest: request.Digest, TeamID: request.TeamID,
		Attempt: request.Attempt, LeaseExpiresAt: request.LeaseExpiresAt,
		CreatedAt: request.LeaseExpiresAt.Add(-time.Hour),
	}, nil
}

func (s *concurrentSealStore) DigestState(_ context.Context, _ DigestLease, digest Digest, _ time.Time) (DigestState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := DigestState{Digest: digest}
	if manifest, found := s.snapshots[digest]; found {
		state.Snapshots = []Snapshot{manifest.Clone()}
		state.Locations = append([]Location(nil), s.locations[digest]...)
	}
	return state, nil
}

func (s *concurrentSealStore) CommitSealBatch(_ context.Context, _ DigestLease, commit SealCommit) (map[string]SealedOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commits++
	result := make(map[string]SealedOutput, len(commit.Outputs))
	for _, output := range commit.Outputs {
		manifest, found := s.snapshots[output.Digest]
		if !found {
			s.created++
			s.nextID++
			manifest = Snapshot{
				ID: s.nextID, Type: output.Port.Type, Digest: output.Digest,
				ByteSize: output.ByteSize, FileCount: output.FileCount,
				Representation: output.Representation, IntrinsicMetadata: output.IntrinsicMetadata,
				ContentState: ContentStateAvailable, CreatedAt: s.now,
			}
			s.snapshots[output.Digest] = manifest
			s.locations[output.Digest] = append([]Location(nil), output.Locations...)
		}
		result[output.ClientKey] = SealedOutput{
			Port:     output.Port,
			Snapshot: SnapshotRef{ID: manifest.ID, Type: manifest.Type, Digest: manifest.Digest},
		}
	}
	return result, nil
}

func (s *concurrentSealStore) counts() (stages, commits, created int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stages, s.commits, s.created
}

type concurrentContentStore struct {
	ContentStore
	mu     sync.Mutex
	puts   int
	stored map[Digest][]byte
}

func newConcurrentContentStore() *concurrentContentStore {
	return &concurrentContentStore{stored: map[Digest][]byte{}}
}

func (s *concurrentContentStore) Put(_ context.Context, digest Digest, reader io.Reader) ([]Location, error) {
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	s.stored[digest] = body
	return []Location{{Digest: digest, Driver: "test", Key: digest.String()}}, nil
}

func (s *concurrentContentStore) Exists(_ context.Context, location Location) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, found := s.stored[location.Digest]
	return found, nil
}

func (s *concurrentContentStore) putCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue is the sealer's
// first concurrent test. Two independent steps produce byte-identical output at
// the same moment through one metadata store, one content store and one digest
// lease manager that genuinely excludes.
//
// What must hold:
//
//   - Both seals succeed. Contention is not an error condition.
//   - Both converge on ONE digest and ONE snapshot ID. A second stored value for
//     identical bytes would silently fork the corpus.
//   - The bytes are stored ONCE. The loser of the race must find the winner's
//     committed, verified locations and reuse them rather than re-uploading.
//   - Exactly one commit CREATES content; the other binds to it.
//   - The lease was never held by both at once. Without that, the three
//     assertions above could pass by luck on a fast machine.
//
// Run under -race as well as plain; plan 01's race lane executes it.
func TestConcurrentIdenticalContentSealsConvergeOnOneStoredValue(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "identical")
	wantDigest := canonicalDigest(t, body)

	metadata := newConcurrentSealStore(now)
	content := newConcurrentContentStore()
	locks := newBlockingDigestLocks()
	validator := sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
		return ValidationResult{}, nil
	})

	const sealers = 2
	results := make([]map[string]SealedOutput, sealers)
	failures := make([]error, sealers)
	start := make(chan struct{})
	var group sync.WaitGroup

	for i := 0; i < sealers; i++ {
		sealer, err := NewBatchSealer(
			Canonicalizer{TempDir: t.TempDir()},
			sealerRegistry{TypeRef("opaque/v1"): validator},
			metadata, content, locks,
			WithBatchSealerClock(func() time.Time { return now }),
		)
		if err != nil {
			t.Fatal(err)
		}
		request := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})
		request.PlanID = fmt.Sprintf("plan-%d", i)

		group.Add(1)
		go func(index int, sealer *BatchSealer, request SealRequest) {
			defer group.Done()
			<-start
			results[index], failures[index] = sealer.Seal(context.Background(), request)
		}(i, sealer, request)
	}
	close(start)
	group.Wait()

	for i, err := range failures {
		if err != nil {
			t.Fatalf("sealer %d failed: %v", i, err)
		}
	}

	first, found := results[0]["result"]
	if !found {
		t.Fatalf("sealer 0 returned %#v", results[0])
	}
	second, found := results[1]["result"]
	if !found {
		t.Fatalf("sealer 1 returned %#v", results[1])
	}
	if first.Snapshot.Digest != wantDigest || second.Snapshot.Digest != wantDigest {
		t.Fatalf("sealed digests = %s and %s, want %s both",
			first.Snapshot.Digest, second.Snapshot.Digest, wantDigest)
	}
	if first.Snapshot.ID != second.Snapshot.ID {
		t.Fatalf("identical content forked into snapshots %s and %s", first.Snapshot.ID, second.Snapshot.ID)
	}

	if puts := content.putCount(); puts != 1 {
		t.Fatalf("content store received %d writes for identical bytes, want exactly 1", puts)
	}
	stages, commits, created := metadata.counts()
	if created != 1 {
		t.Fatalf("%d commits created content, want exactly 1", created)
	}
	if commits != sealers {
		t.Fatalf("commits = %d, want one per production (%d)", commits, sealers)
	}
	if stages != sealers {
		t.Fatalf("stages = %d, want one per sealer (%d)", stages, sealers)
	}

	acquires, maxHeld := locks.stats()
	if acquires != sealers {
		t.Fatalf("digest lease acquisitions = %d, want %d", acquires, sealers)
	}
	if maxHeld != 1 {
		t.Fatalf("the digest lease was held by %d callers at once; the lease does not exclude", maxHeld)
	}
}
