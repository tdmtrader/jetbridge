package snapshots

import (
	"code.cloudfoundry.org/lager/v3"
	"context"
	"io"
	"net/http"

	"github.com/concourse/concourse/agent/projection"
	"github.com/concourse/concourse/agent/resourcecapture"
	"github.com/concourse/concourse/agent/snapshot"
)

// ErrorResponse is the bounded public error envelope used by every handler.
// Handler code supplies stable messages rather than serializing dependency
// errors, which may contain tenant or storage details.
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
}

// PinRequest is the only JSON body accepted by the snapshot API. Actor and
// snapshot identity are intentionally absent and always server-derived.
type PinRequest struct {
	Reason string `json:"reason"`
}

// TrustedTeam is resolved by the ATC team-scoped handler before this package
// handles a request. Team identity is never accepted from an HTTP body.
type TrustedTeam struct {
	ID   int
	Name string
}

// RequestIdentity is derived from verified authentication state by ATC
// wiring. Actor is the stable connector/subject ownership key; DisplayName is
// audit-only and may change without changing pin ownership.
type RequestIdentity struct {
	Actor       string
	DisplayName string
}

type RequestIdentityFunc func(*http.Request) (RequestIdentity, error)

// SnapshotCreator is the narrow upload half of snapshot.SnapshotCreator.
type SnapshotCreator interface {
	Upload(context.Context, snapshot.UploadRequest) (snapshot.Snapshot, error)
}

type ResourceCapturer interface {
	Capture(context.Context, resourcecapture.Request) (resourcecapture.CaptureResult, error)
}

// MetadataStore is the authorization-filtered read and actor-pin surface used
// by the HTTP API. The root snapshot.MetadataStore satisfies this interface.
type MetadataStore interface {
	GetAuthorized(context.Context, int, snapshot.SnapshotID) (snapshot.Snapshot, bool, error)
	GetAuthorizedDetail(context.Context, int, snapshot.SnapshotID) (snapshot.Detail, bool, error)
	ListAuthorized(context.Context, int, snapshot.SnapshotListFilter) ([]snapshot.Snapshot, error)
	Pin(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef, string) (snapshot.RetentionClaim, error)
	Unpin(context.Context, snapshot.DigestLease, int, string, snapshot.SnapshotRef) error
}

// ContentStore deliberately exposes only authorized content opening. Upload
// and lifecycle mutation stay behind the snapshot-domain creator.
type ContentStore interface {
	Open(context.Context, snapshot.Snapshot) (io.ReadCloser, error)
}

type RepositoryChangeProjectionReader interface {
	GetRepositoryChangeProjection(context.Context, snapshot.SnapshotID) (projection.RepositoryChange, bool, error)
}

type ErrorReporter func(context.Context, string)

// Config controls construction of the team-bound snapshot handlers.
type Config struct {
	// Logger receives the causes behind content_unavailable and other handler
	// failures. Optional; NewHandlerFactory substitutes a sink-less logger.
	Logger            lager.Logger
	Enabled           bool
	Creator           SnapshotCreator
	Metadata          MetadataStore
	Content           ContentStore
	RepositoryChanges RepositoryChangeProjectionReader
	Locks             snapshot.DigestLockManager
	ArchiveLimits     snapshot.ArchiveLimits
	// TempDir is the dedicated disk-backed snapshot scratch directory. Content
	// downloads are fully staged and digest-verified here before any success
	// headers are committed to a client.
	TempDir     string
	Identity    RequestIdentityFunc
	ReportError ErrorReporter
}
