package snapshot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

const defaultLifecyclePageSize = 100

var (
	ErrNoReadableReplica    = errors.New("snapshot: no readable replica")
	ErrReplicaMissing       = errors.New("snapshot: replica missing")
	ErrReplicaCorrupt       = errors.New("snapshot: replica corrupt")
	ErrLifecycleStateChange = errors.New("snapshot: lifecycle state changed")
)

type LifecyclePhase string

const (
	LifecyclePhaseLock         LifecyclePhase = "lock"
	LifecyclePhaseRecheck      LifecyclePhase = "recheck"
	LifecyclePhaseDelete       LifecyclePhase = "delete"
	LifecyclePhaseStageCleanup LifecyclePhase = "stage-cleanup"
	LifecyclePhaseExpire       LifecyclePhase = "expire"
	LifecyclePhaseVerify       LifecyclePhase = "verify"
	LifecyclePhaseCopy         LifecyclePhase = "copy"
	LifecyclePhaseMetadata     LifecyclePhase = "metadata"
)

func (p LifecyclePhase) Validate() error {
	switch p {
	case LifecyclePhaseLock, LifecyclePhaseRecheck, LifecyclePhaseDelete,
		LifecyclePhaseStageCleanup, LifecyclePhaseExpire, LifecyclePhaseVerify,
		LifecyclePhaseCopy, LifecyclePhaseMetadata:
		return nil
	default:
		return fmt.Errorf("snapshot: invalid lifecycle phase %q", p)
	}
}

type LifecycleCandidateError struct {
	Candidate LifecycleCandidate
	Phase     LifecyclePhase
	Err       error
}

func (e LifecycleCandidateError) Error() string {
	return fmt.Sprintf("snapshot: lifecycle %s for %s/%s: %v", e.Phase, e.Candidate.Digest, e.Candidate.Kind, e.Err)
}

func (e LifecycleCandidateError) Unwrap() error { return e.Err }

type ReplicaRepairResult struct {
	Added        []Location
	Removed      []Location
	Verified     int
	Desired      int
	LiveCapacity int
}

func (r ReplicaRepairResult) Validate(digest Digest) error {
	if err := digest.Validate(); err != nil {
		return err
	}
	if r.Verified < 0 || r.Desired < 0 || r.LiveCapacity < 0 || r.Desired > r.LiveCapacity {
		return fmt.Errorf("snapshot: invalid replica repair counts")
	}
	for _, locations := range [][]Location{r.Added, r.Removed} {
		for _, location := range locations {
			if err := location.Validate(); err != nil {
				return err
			}
			if location.Digest != digest {
				return fmt.Errorf("snapshot: replica repair location belongs to another digest")
			}
		}
	}
	return nil
}

type ReplicaRepairer interface {
	RepairReplicas(context.Context, Snapshot, []Location) (ReplicaRepairResult, error)
}

type LifecycleReport struct {
	Scanned          int
	Deferred         int
	Failed           int
	StagesRemoved    int
	DigestsExpired   int
	LocationsDeleted int
	LocationsAdded   int
	StalePruned      int
}

type Lifecycle struct {
	metadata MetadataStore
	content  ContentStore
	replicas ReplicaRepairer
	locks    DigestLockManager
	now      func() time.Time
	pageSize int

	collectMu     sync.Mutex
	collectCursor LifecycleCursor
	repairMu      sync.Mutex
	repairCursor  LifecycleCursor
}

type LifecycleOption func(*Lifecycle)

func WithLifecycleClock(now func() time.Time) LifecycleOption {
	return func(lifecycle *Lifecycle) { lifecycle.now = now }
}

func WithLifecyclePageSize(size int) LifecycleOption {
	return func(lifecycle *Lifecycle) { lifecycle.pageSize = size }
}

func NewLifecycle(
	metadata MetadataStore,
	content ContentStore,
	replicas ReplicaRepairer,
	locks DigestLockManager,
	options ...LifecycleOption,
) (*Lifecycle, error) {
	if interfaceIsNil(metadata) {
		return nil, fmt.Errorf("snapshot: lifecycle metadata store is required")
	}
	if interfaceIsNil(content) {
		return nil, fmt.Errorf("snapshot: lifecycle content store is required")
	}
	if interfaceIsNil(replicas) {
		return nil, fmt.Errorf("snapshot: lifecycle replica repairer is required")
	}
	if interfaceIsNil(locks) {
		return nil, fmt.Errorf("snapshot: lifecycle digest lock manager is required")
	}
	lifecycle := &Lifecycle{
		metadata: metadata,
		content:  content,
		replicas: replicas,
		locks:    locks,
		now:      time.Now,
		pageSize: defaultLifecyclePageSize,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("snapshot: lifecycle option is required")
		}
		option(lifecycle)
	}
	if lifecycle.now == nil {
		return nil, fmt.Errorf("snapshot: lifecycle clock is required")
	}
	if lifecycle.pageSize <= 0 || lifecycle.pageSize > MaxLifecyclePageSize {
		return nil, fmt.Errorf("snapshot: lifecycle page size must be between 1 and %d", MaxLifecyclePageSize)
	}
	return lifecycle, nil
}

func (lifecycle *Lifecycle) Collect(ctx context.Context) (LifecycleReport, error) {
	if lifecycle == nil {
		return LifecycleReport{}, fmt.Errorf("snapshot: lifecycle is required")
	}
	lifecycle.collectMu.Lock()
	defer lifecycle.collectMu.Unlock()
	return lifecycle.runPage(ctx, &lifecycle.collectCursor, func(candidate LifecycleCandidate) bool {
		return candidate.Kind == LifecycleCandidateOrphan || candidate.Kind == LifecycleCandidateExpiry
	}, func(
		ctx context.Context,
		candidate LifecycleCandidate,
		now time.Time,
		lease DigestLease,
		phase *LifecyclePhase,
		report *LifecycleReport,
	) (bool, error) {
		switch candidate.Kind {
		case LifecycleCandidateOrphan:
			return lifecycle.collectOrphan(ctx, candidate.Digest, now, lease, phase, report)
		case LifecycleCandidateExpiry:
			return lifecycle.collectExpiry(ctx, candidate.Digest, now, lease, phase, report)
		default:
			return false, nil
		}
	})
}

func (lifecycle *Lifecycle) Repair(ctx context.Context) (LifecycleReport, error) {
	if lifecycle == nil {
		return LifecycleReport{}, fmt.Errorf("snapshot: lifecycle is required")
	}
	lifecycle.repairMu.Lock()
	defer lifecycle.repairMu.Unlock()
	return lifecycle.runPage(ctx, &lifecycle.repairCursor, func(candidate LifecycleCandidate) bool {
		return candidate.Kind == LifecycleCandidateRepair
	}, func(
		ctx context.Context,
		candidate LifecycleCandidate,
		now time.Time,
		lease DigestLease,
		phase *LifecyclePhase,
		report *LifecycleReport,
	) (bool, error) {
		if candidate.Kind != LifecycleCandidateRepair {
			return false, nil
		}
		return lifecycle.repairCandidate(ctx, candidate.Digest, now, lease, phase, report)
	})
}

type lifecycleCandidateProcessor func(
	context.Context,
	LifecycleCandidate,
	time.Time,
	DigestLease,
	*LifecyclePhase,
	*LifecycleReport,
) (bool, error)

type lifecycleCandidateMatcher func(LifecycleCandidate) bool

func (lifecycle *Lifecycle) runPage(
	ctx context.Context,
	cursor *LifecycleCursor,
	matches lifecycleCandidateMatcher,
	process lifecycleCandidateProcessor,
) (LifecycleReport, error) {
	request := LifecyclePageRequest{After: *cursor, Limit: lifecycle.pageSize}
	page, err := lifecycle.metadata.DiscoverLifecycleCandidates(ctx, request)
	if err != nil {
		return LifecycleReport{}, err
	}
	if err := page.Validate(request); err != nil {
		return LifecycleReport{}, fmt.Errorf("snapshot: invalid lifecycle discovery page: %w", err)
	}

	report := LifecycleReport{}
	failures := []error{}
	last := request.After
	for _, candidate := range page.Candidates {
		if err := ctx.Err(); err != nil {
			*cursor = last
			return report, err
		}
		report.Scanned++
		last = candidate.Cursor()
		if !matches(candidate) {
			report.Deferred++
			continue
		}
		phase := LifecyclePhaseLock
		processed := false
		candidateNow := lifecycle.now()
		err := WithDigestLease(ctx, lifecycle.locks, []Digest{candidate.Digest}, func(lease DigestLease) error {
			var err error
			processed, err = process(ctx, candidate, candidateNow, lease, &phase, &report)
			return err
		})
		if err != nil {
			if contextError(ctx, err) != nil {
				*cursor = last
				return report, contextError(ctx, err)
			}
			report.Failed++
			failures = append(failures, LifecycleCandidateError{Candidate: candidate, Phase: phase, Err: err})
			continue
		}
		if !processed {
			report.Deferred++
		}
	}
	*cursor = page.Next
	return report, errors.Join(failures...)
}

func (lifecycle *Lifecycle) collectOrphan(
	ctx context.Context,
	digest Digest,
	now time.Time,
	lease DigestLease,
	phase *LifecyclePhase,
	report *LifecycleReport,
) (bool, error) {
	*phase = LifecyclePhaseRecheck
	state, err := lifecycle.metadata.DigestState(ctx, lease, digest, now)
	if err != nil {
		return false, err
	}
	if err := state.Validate(); err != nil {
		return false, err
	}
	expired := make([]int64, 0, len(state.Stages))
	hasUnexpired := false
	for _, stage := range state.Stages {
		if stage.LeaseExpiresAt.After(now) {
			hasUnexpired = true
			continue
		}
		expired = append(expired, stage.ID)
	}
	if len(expired) == 0 {
		return false, nil
	}
	if !state.HasManifest() {
		if hasUnexpired {
			return false, nil
		}
		*phase = LifecyclePhaseDelete
		if err := lifecycle.content.DeleteAll(ctx, digest); err != nil {
			return true, err
		}
	}
	*phase = LifecyclePhaseStageCleanup
	if err := lifecycle.metadata.RemoveStagedUploads(ctx, lease, digest, expired); err != nil {
		return true, err
	}
	report.StagesRemoved += len(expired)
	return true, nil
}

func (lifecycle *Lifecycle) collectExpiry(
	ctx context.Context,
	digest Digest,
	now time.Time,
	lease DigestLease,
	phase *LifecyclePhase,
	report *LifecycleReport,
) (bool, error) {
	*phase = LifecyclePhaseRecheck
	state, err := lifecycle.metadata.DigestState(ctx, lease, digest, now)
	if err != nil {
		return false, err
	}
	if err := state.Validate(); err != nil {
		return false, err
	}
	if !state.Available() || state.HasActiveRetention || state.HasUnexpiredStage(now) {
		return false, nil
	}

	locations := append([]Location(nil), state.Locations...)
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].Driver != locations[j].Driver {
			return locations[i].Driver < locations[j].Driver
		}
		if locations[i].Key != locations[j].Key {
			return locations[i].Key < locations[j].Key
		}
		return locations[i].Node < locations[j].Node
	})
	for _, location := range locations {
		*phase = LifecyclePhaseDelete
		if err := lifecycle.content.DeleteLocation(ctx, location); err != nil {
			return true, err
		}
		report.LocationsDeleted++
		*phase = LifecyclePhaseMetadata
		if err := lifecycle.metadata.RemoveLocation(ctx, lease, location); err != nil {
			return true, err
		}
	}

	*phase = LifecyclePhaseRecheck
	state, err = lifecycle.metadata.DigestState(ctx, lease, digest, now)
	if err != nil {
		return true, err
	}
	if err := state.Validate(); err != nil {
		return true, err
	}
	if !state.Available() || state.HasActiveRetention || state.HasUnexpiredStage(now) || len(state.Locations) != 0 {
		return true, ErrLifecycleStateChange
	}
	*phase = LifecyclePhaseDelete
	if err := lifecycle.content.DeleteAll(ctx, digest); err != nil {
		return true, err
	}
	*phase = LifecyclePhaseExpire
	expired, err := lifecycle.metadata.MarkDigestExpired(ctx, lease, digest, now)
	if err != nil {
		return true, err
	}
	if !expired {
		return true, ErrLifecycleStateChange
	}
	report.DigestsExpired++
	return true, nil
}

func (lifecycle *Lifecycle) repairCandidate(
	ctx context.Context,
	digest Digest,
	now time.Time,
	lease DigestLease,
	phase *LifecyclePhase,
	report *LifecycleReport,
) (bool, error) {
	*phase = LifecyclePhaseRecheck
	state, err := lifecycle.metadata.DigestState(ctx, lease, digest, now)
	if err != nil {
		return false, err
	}
	if err := state.Validate(); err != nil {
		return false, err
	}
	if !state.Available() || !state.HasActiveRetention || len(state.Snapshots) == 0 {
		return false, nil
	}
	manifests := append([]Snapshot(nil), state.Snapshots...)
	sort.Slice(manifests, func(i, j int) bool {
		if manifests[i].Type != manifests[j].Type {
			return manifests[i].Type < manifests[j].Type
		}
		return manifests[i].ID < manifests[j].ID
	})
	locations := append([]Location(nil), state.Locations...)
	sort.Slice(locations, func(i, j int) bool {
		if locations[i].Driver != locations[j].Driver {
			return locations[i].Driver < locations[j].Driver
		}
		if locations[i].Key != locations[j].Key {
			return locations[i].Key < locations[j].Key
		}
		return locations[i].Node < locations[j].Node
	})
	*phase = LifecyclePhaseVerify
	result, repairErr := lifecycle.replicas.RepairReplicas(ctx, manifests[0], locations)
	if err := result.Validate(digest); err != nil {
		return true, errors.Join(repairErr, err)
	}
	repairPhase := LifecyclePhaseVerify
	if repairErr != nil && !errors.Is(repairErr, ErrNoReadableReplica) {
		repairPhase = LifecyclePhaseCopy
		*phase = repairPhase
	}
	var metadataErrors []error
	for _, location := range result.Added {
		*phase = LifecyclePhaseMetadata
		if err := lifecycle.metadata.AddLocation(ctx, lease, location); err != nil {
			metadataErrors = append(metadataErrors, err)
			break
		}
		report.LocationsAdded++
	}
	if len(metadataErrors) == 0 {
		for _, location := range result.Removed {
			*phase = LifecyclePhaseMetadata
			if err := lifecycle.metadata.RemoveLocation(ctx, lease, location); err != nil {
				metadataErrors = append(metadataErrors, err)
				break
			}
			report.StalePruned++
		}
	}
	if len(metadataErrors) == 0 {
		*phase = repairPhase
	}
	return true, errors.Join(repairErr, errors.Join(metadataErrors...))
}

func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(err, ctxErr) {
		return ctxErr
	}
	return nil
}
