package snapshot

import (
	"context"
	"io"
	"time"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

// StagedUpload records a lease-protected attempt to store one digest before it
// is made visible by CommitSealBatch. A crash can leave this record behind for
// lifecycle recovery.
type StagedUpload struct {
	ID             int64     `json:"id"`
	Digest         string    `json:"digest"`
	TeamID         int       `json:"team_id"`
	Attempt        string    `json:"attempt"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// SealCommit is the one metadata mutation that may expose new snapshots. It
// consumes StagedUpload records only after the content locations are durable,
// and creates snapshots, productions, grants, retention claims, lineage, and
// workflow-run output bindings in one transaction.
type SealCommit struct {
	Request       SealRequest      `json:"request"`
	Snapshots     []Snapshot       `json:"snapshots"`
	Locations     []Location       `json:"locations"`
	Productions   []Production     `json:"productions"`
	Grants        []Grant          `json:"grants"`
	Claims        []RetentionClaim `json:"claims"`
	Lineage       []LineageEdge    `json:"lineage"`
	StagedUploads []StagedUpload   `json:"staged_uploads"`
}

// SnapshotListFilter bounds an authorized list query. Empty fields do not
// filter; callers still receive only values granted to their team.
type SnapshotListFilter struct {
	Type         TypeRef
	ContentState ContentState
	CreatedAfter *time.Time
	Limit        int
}

// MetadataStore owns durable snapshot metadata. Its implementation is allowed
// to use PostgreSQL, but this package is deliberately independent of it.
//
// Lock protocol: callers MUST hold the matching DigestLease across StageUpload
// and CommitSealBatch. Lifecycle callers MUST hold the same lease across their
// final staged-upload recheck, ContentStore.DeleteAll, and RemoveStagedUploads.
// The lease spans external content-store I/O; an in-process mutex is not an
// equivalent substitute.
//
//counterfeiter:generate . MetadataStore
type MetadataStore interface {
	// StageUpload durably records an upload attempt before external content is
	// written. The caller must hold the matching digest lease.
	StageUpload(StagedUpload) error
	// CommitSealBatch is the only mutation that exposes snapshots. The caller
	// must hold matching digest leases for every digest in commit.
	CommitSealBatch(SealCommit) (map[string]SealedOutput, error)

	// StagedUploads returns staged attempts for lifecycle recovery.
	StagedUploads(now time.Time) ([]StagedUpload, error)
	// RemoveStagedUploads deletes stale stage rows. For orphan cleanup the
	// caller must hold the matching digest lease through external deletion.
	RemoveStagedUploads([]int64) error

	// GetAuthorized and ListAuthorized enforce durable team grants. Knowing an
	// ID never by itself grants content access.
	GetAuthorized(teamID int, id SnapshotID) (Snapshot, bool, error)
	ListAuthorized(teamID int, filter SnapshotListFilter) ([]Snapshot, error)

	// Pin and Unpin are actor-scoped: one actor cannot remove another actor's
	// independent retention claim.
	Pin(teamID int, actor string, id SnapshotID, reason string) (RetentionClaim, error)
	Unpin(teamID int, actor string, id SnapshotID) error

	// ActiveRetentionClaims and ExpiredRetentionClaims support lifecycle scans.
	// Grants and lineage are intentionally excluded: neither retains bytes.
	ActiveRetentionClaims(now time.Time) ([]RetentionClaim, error)
	ExpiredRetentionClaims(now time.Time) ([]RetentionClaim, error)

	// MarkContentExpired preserves immutable metadata/provenance while making
	// content unavailable after the final retention claim is gone.
	MarkContentExpired(id SnapshotID) error
	// AddLocation and RemoveLocation let lifecycle repair replica metadata.
	AddLocation(Location) error
	RemoveLocation(Location) error
}

// ContentStore holds immutable canonical archive bytes independently of
// MetadataStore and PostgreSQL.
//
//counterfeiter:generate . ContentStore
type ContentStore interface {
	Put(ctx context.Context, digest string, archive io.Reader) ([]Location, error)
	Open(ctx context.Context, snapshot Snapshot) (io.ReadCloser, error)
	Exists(ctx context.Context, location Location) (bool, error)
	DeleteLocation(ctx context.Context, location Location) error
	// DeleteAll broadcasts a digest deletion to live storage peers. Lifecycle
	// recovery must call it while holding the matching DigestLease so staged
	// uploads remain recoverable even when a crash occurred before locations
	// were recorded and a newly committed location cannot race physical deletion.
	DeleteAll(ctx context.Context, digest string) error
}

// DigestLease represents PostgreSQL session-scoped advisory locks. Close
// releases all locks and returns the dedicated database session to its pool.
type DigestLease interface {
	Close() error
}

// DigestLockManager serializes a sorted, unique set of snapshot digests across
// staged upload, external content I/O, and metadata commit. Implementations
// must hold session-scoped locks until the returned lease is closed.
type DigestLockManager interface {
	AcquireMany(ctx context.Context, sortedUniqueDigests []string) (DigestLease, error)
}
