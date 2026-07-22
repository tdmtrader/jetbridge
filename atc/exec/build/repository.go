package build

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/runtime"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// ArtifactName is just a string, with its own type to make interfaces using it
// more self-documenting.
type ArtifactName string

type ArtifactEntry struct {
	Artifact  runtime.Artifact
	FromCache bool
	Snapshot  *snapshot.SnapshotRef
}

var (
	ErrArtifactAlreadyRegistered = errors.New("artifact already registered in local scope")
	ErrArtifactCommitConflict    = errors.New("artifact repository commit conflict")
	ErrArtifactScopeCommitted    = errors.New("artifact repository scope already committed")
	ErrArtifactScopeClosed       = errors.New("artifact repository scope commit already attempted")
	ErrArtifactScopeHasNoParent  = errors.New("artifact repository scope has no parent")
)

type commitState uint8

const (
	commitStateOpen commitState = iota
	commitStateAttempting
	commitStateCommitted
	commitStateFailed
)

// Repository is the mapping from an ArtifactName to a complete ArtifactEntry.
// All entries and image references are protected by one mutex and replaced by
// copy-on-write, so a reader observes one coherent repository generation.
type Repository struct {
	mu sync.RWMutex

	entries   map[ArtifactName]ArtifactEntry
	imageRefs map[ArtifactName]string
	revisions map[ArtifactName]uint64

	parent        *Repository
	baseRevisions map[ArtifactName]uint64
	dirtyEntries  map[ArtifactName]struct{}
	dirtyImages   map[ArtifactName]struct{}
	commitState   commitState
}

// NewRepository constructs a new root repository.
func NewRepository() *Repository {
	return &Repository{
		entries:   map[ArtifactName]ArtifactEntry{},
		imageRefs: map[ArtifactName]string{},
		revisions: map[ArtifactName]uint64{},
	}
}

// RegisterArtifacts validates and deep-copies all snapshot references before
// publishing the entire batch as one generation. Root repositories retain the
// historical last-writer-wins behavior. A local scope may replace an inherited
// name once; registering that artifact name again in the same scope rejects the
// whole batch.
//
// A map cannot represent duplicate keys that were collapsed by its caller.
// Callers collecting outputs must therefore maintain a seen-set before building
// this map.
func (repo *Repository) RegisterArtifacts(entries map[ArtifactName]ArtifactEntry) error {
	prepared, err := prepareEntries(entries)
	if err != nil {
		return err
	}

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if err := repo.ensureWritable(); err != nil {
		return err
	}
	if repo.parent != nil {
		for _, name := range sortedEntryNames(prepared) {
			if _, dirty := repo.dirtyEntries[name]; dirty {
				return fmt.Errorf("%w: %q", ErrArtifactAlreadyRegistered, name)
			}
		}
	}
	if len(prepared) == 0 {
		return nil
	}

	nextEntries := cloneEntries(repo.entries)
	nextRevisions := cloneRevisions(repo.revisions)
	for name, entry := range prepared {
		nextEntries[name] = entry
		nextRevisions[name]++
	}
	repo.entries = nextEntries
	repo.revisions = nextRevisions

	if repo.parent != nil {
		nextDirty := cloneNames(repo.dirtyEntries)
		for name := range prepared {
			nextDirty[name] = struct{}{}
		}
		repo.dirtyEntries = nextDirty
	}
	return nil
}

// RegisterArtifact is the compatibility path for legacy producers. It is
// last-writer-wins at the root. In a local scope it delegates to the checked
// one-entry path; because this legacy signature cannot return an error, a
// duplicate local write is ignored and the first local value is preserved.
func (repo *Repository) RegisterArtifact(name ArtifactName, artifact runtime.Artifact, fromCache bool) {
	_ = repo.RegisterArtifacts(map[ArtifactName]ArtifactEntry{
		name: {Artifact: artifact, FromCache: fromCache},
	})
}

// RegisterImageRef stores an image reference URL. Image references share the
// repository generation/revision lock with artifacts so retry scopes copy,
// discard, conflict-check, and commit both forms of identity together. As with
// RegisterArtifact, a second local legacy write is ignored.
func (repo *Repository) RegisterImageRef(name ArtifactName, imageURL string) {
	repo.mu.Lock()
	defer repo.mu.Unlock()

	if repo.ensureWritable() != nil {
		return
	}
	if repo.parent != nil {
		if _, dirty := repo.dirtyImages[name]; dirty {
			return
		}
	}

	nextImages := cloneImageRefs(repo.imageRefs)
	nextRevisions := cloneRevisions(repo.revisions)
	nextImages[name] = imageURL
	nextRevisions[name]++
	repo.imageRefs = nextImages
	repo.revisions = nextRevisions

	if repo.parent != nil {
		nextDirty := cloneNames(repo.dirtyImages)
		nextDirty[name] = struct{}{}
		repo.dirtyImages = nextDirty
	}
}

// ImageRefFor looks up the image reference URL for the captured repository
// generation. Local scopes never dynamically fall through to their parent.
func (repo *Repository) ImageRefFor(name ArtifactName) (string, bool) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	value, found := repo.imageRefs[name]
	return value, found
}

// ArtifactEntryFor returns the complete entry from one repository generation.
// The SnapshotRef pointer is always a fresh copy.
func (repo *Repository) ArtifactEntryFor(name ArtifactName) (ArtifactEntry, bool) {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	entry, found := repo.entries[name]
	if !found {
		return ArtifactEntry{}, false
	}
	return cloneEntry(entry), true
}

// ArtifactFor is the legacy convenience lookup.
func (repo *Repository) ArtifactFor(name ArtifactName) (runtime.Artifact, bool, bool) {
	entry, found := repo.ArtifactEntryFor(name)
	if !found {
		return nil, false, false
	}
	return entry.Artifact, entry.FromCache, true
}

// SnapshotFor is a convenience lookup returning a value copy. Typed consumers
// that also need the artifact must call ArtifactEntryFor so both are read from
// the same generation.
func (repo *Repository) SnapshotFor(name ArtifactName) (snapshot.SnapshotRef, bool) {
	entry, found := repo.ArtifactEntryFor(name)
	if !found || entry.Snapshot == nil {
		return snapshot.SnapshotRef{}, false
	}
	return *entry.Snapshot, true
}

// AsMap returns a deep copy of the complete captured artifact map.
func (repo *Repository) AsMap() map[ArtifactName]ArtifactEntry {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	return cloneEntries(repo.entries)
}

// NewLocalScope captures one coherent immutable snapshot of artifacts, image
// references, and per-name revisions. Subsequent parent changes are invisible
// to the child.
func (repo *Repository) NewLocalScope() *Repository {
	repo.mu.RLock()
	defer repo.mu.RUnlock()
	return &Repository{
		entries:       cloneEntries(repo.entries),
		imageRefs:     cloneImageRefs(repo.imageRefs),
		revisions:     cloneRevisions(repo.revisions),
		parent:        repo,
		baseRevisions: cloneRevisions(repo.revisions),
		dirtyEntries:  map[ArtifactName]struct{}{},
		dirtyImages:   map[ArtifactName]struct{}{},
	}
}

// CommitToParent is a single-use transaction. It publishes all dirty artifact
// entries and image refs together, or nothing. A repeated call after success
// returns ErrArtifactScopeCommitted; a repeated call after a failed/in-progress
// attempt returns ErrArtifactScopeClosed.
func (repo *Repository) CommitToParent() error {
	repo.mu.Lock()
	if repo.parent == nil {
		repo.mu.Unlock()
		return ErrArtifactScopeHasNoParent
	}
	switch repo.commitState {
	case commitStateCommitted:
		repo.mu.Unlock()
		return ErrArtifactScopeCommitted
	case commitStateAttempting, commitStateFailed:
		repo.mu.Unlock()
		return ErrArtifactScopeClosed
	}
	repo.commitState = commitStateAttempting
	parent := repo.parent
	baseRevisions := cloneRevisions(repo.baseRevisions)
	dirtyEntries := cloneNames(repo.dirtyEntries)
	dirtyImages := cloneNames(repo.dirtyImages)
	entries := cloneEntries(repo.entries)
	imageRefs := cloneImageRefs(repo.imageRefs)
	repo.mu.Unlock()

	commitErr := commitSnapshot(parent, baseRevisions, dirtyEntries, dirtyImages, entries, imageRefs)

	repo.mu.Lock()
	if commitErr == nil {
		repo.commitState = commitStateCommitted
	} else {
		repo.commitState = commitStateFailed
	}
	repo.mu.Unlock()
	return commitErr
}

func (repo *Repository) Parent() *Repository {
	return repo.parent
}

func commitSnapshot(
	parent *Repository,
	baseRevisions map[ArtifactName]uint64,
	dirtyEntries map[ArtifactName]struct{},
	dirtyImages map[ArtifactName]struct{},
	entries map[ArtifactName]ArtifactEntry,
	imageRefs map[ArtifactName]string,
) error {
	parent.mu.Lock()
	defer parent.mu.Unlock()

	if err := parent.ensureWritable(); err != nil {
		return err
	}
	dirtyNames := unionNames(dirtyEntries, dirtyImages)
	for _, name := range sortedNames(dirtyNames) {
		if parent.revisions[name] != baseRevisions[name] {
			return fmt.Errorf("%w: %q", ErrArtifactCommitConflict, name)
		}
	}

	nextEntries := cloneEntries(parent.entries)
	nextImages := cloneImageRefs(parent.imageRefs)
	nextRevisions := cloneRevisions(parent.revisions)
	for name := range dirtyEntries {
		nextEntries[name] = cloneEntry(entries[name])
	}
	for name := range dirtyImages {
		nextImages[name] = imageRefs[name]
	}
	for name := range dirtyNames {
		nextRevisions[name]++
	}

	parent.entries = nextEntries
	parent.imageRefs = nextImages
	parent.revisions = nextRevisions
	if parent.parent != nil {
		nextDirtyEntries := cloneNames(parent.dirtyEntries)
		nextDirtyImages := cloneNames(parent.dirtyImages)
		for name := range dirtyEntries {
			nextDirtyEntries[name] = struct{}{}
		}
		for name := range dirtyImages {
			nextDirtyImages[name] = struct{}{}
		}
		parent.dirtyEntries = nextDirtyEntries
		parent.dirtyImages = nextDirtyImages
	}
	return nil
}

func (repo *Repository) ensureWritable() error {
	switch repo.commitState {
	case commitStateCommitted:
		return ErrArtifactScopeCommitted
	case commitStateAttempting, commitStateFailed:
		return ErrArtifactScopeClosed
	default:
		return nil
	}
}

func prepareEntries(entries map[ArtifactName]ArtifactEntry) (map[ArtifactName]ArtifactEntry, error) {
	prepared := make(map[ArtifactName]ArtifactEntry, len(entries))
	for _, name := range sortedEntryNames(entries) {
		entry := entries[name]
		if entry.Snapshot != nil {
			if err := entry.Snapshot.Validate(); err != nil {
				return nil, fmt.Errorf("artifact %q snapshot: %w", name, err)
			}
		}
		prepared[name] = cloneEntry(entry)
	}
	return prepared, nil
}

func cloneEntry(entry ArtifactEntry) ArtifactEntry {
	cloned := entry
	if entry.Snapshot != nil {
		ref := *entry.Snapshot
		cloned.Snapshot = &ref
	}
	return cloned
}

func cloneEntries(source map[ArtifactName]ArtifactEntry) map[ArtifactName]ArtifactEntry {
	result := make(map[ArtifactName]ArtifactEntry, len(source))
	for name, entry := range source {
		result[name] = cloneEntry(entry)
	}
	return result
}

func cloneImageRefs(source map[ArtifactName]string) map[ArtifactName]string {
	result := make(map[ArtifactName]string, len(source))
	for name, value := range source {
		result[name] = value
	}
	return result
}

func cloneRevisions(source map[ArtifactName]uint64) map[ArtifactName]uint64 {
	result := make(map[ArtifactName]uint64, len(source))
	for name, revision := range source {
		result[name] = revision
	}
	return result
}

func cloneNames(source map[ArtifactName]struct{}) map[ArtifactName]struct{} {
	result := make(map[ArtifactName]struct{}, len(source))
	for name := range source {
		result[name] = struct{}{}
	}
	return result
}

func unionNames(left, right map[ArtifactName]struct{}) map[ArtifactName]struct{} {
	result := cloneNames(left)
	for name := range right {
		result[name] = struct{}{}
	}
	return result
}

func sortedNames(names map[ArtifactName]struct{}) []ArtifactName {
	result := make([]ArtifactName, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func sortedEntryNames[T any](entries map[ArtifactName]T) []ArtifactName {
	result := make([]ArtifactName, 0, len(entries))
	for name := range entries {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
