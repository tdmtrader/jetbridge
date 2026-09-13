// Package objectstore is the object-level seam the output plane's four cloud
// roles share.
//
// It exists for two reasons the foundation's own adapter could not serve.
//
// First, packaging. hangar/gcs declares the same shape unexported
// (gcs.go:668-679), so hangar/output/{publisher,inventory,reclaimer,policy}
// could not reach it, and each would have grown its own fake and its own
// conformance suite -- four descriptions of one API, drifting.
//
// Second, direction. This package names no cloud SDK type. hangar/gcs stays the
// only importer of cloud.google.com/go/storage in the repository, the role
// packages depend on an interface instead of a client, and ./cmd/concourse does
// not regain a hundred megabytes of transitive dependency because somebody
// wired a role package into the ATC.
//
// The precondition vocabulary is deliberately the GCS one -- DoesNotExist,
// GenerationMatch, MetagenerationMatch -- rather than a generic abstraction.
// Requirement 19 admits the strict native-GCS profile and only that profile,
// because create-if-absent at an exact generation is the whole basis of the
// collision guarantee. An interface that could be implemented by a store
// without those preconditions would be an interface that let one in.
package objectstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/hangar"
)

// The typed status split. Requirement 27 says absence, authorization failure
// and precondition failure stay distinct and none becomes a cache miss, and
// this is where a transport's 404, 403 and 412 stop being numbers.
var (
	// ErrNotFound is 404: no object, or no object at that exact generation.
	ErrNotFound = hangar.ErrNotFound

	// ErrUnauthorized is 403. It is never retried and never widened into an
	// unconditional operation.
	ErrUnauthorized = hangar.ErrUnauthorized

	// ErrInfrastructure is everything else the transport reported.
	ErrInfrastructure = hangar.ErrInfrastructure

	// ErrPreconditionFailed is 412: the object's generation or metageneration
	// is not the one the operation named. For a create-if-absent it means
	// something is already at the key, which is a collision candidate and
	// never a licence to overwrite.
	ErrPreconditionFailed = errors.New("hangar/objectstore: precondition failed")
)

// Conditions is the precondition set an operation runs under.
//
// The zero value is "unconditional", which no output-plane role may use: every
// create names DoesNotExist and every stat, read and delete names an exact
// GenerationMatch. Validate says so, so a role that forgot is refused by its
// own adapter rather than by the bucket's contents.
type Conditions struct {
	DoesNotExist        bool
	GenerationMatch     int64
	MetagenerationMatch int64
}

// IsUnconditional reports the shape no role may issue.
func (conditions Conditions) IsUnconditional() bool {
	return !conditions.DoesNotExist &&
		conditions.GenerationMatch == 0 &&
		conditions.MetagenerationMatch == 0
}

func (conditions Conditions) Validate() error {
	if conditions.IsUnconditional() {
		return fmt.Errorf("%w: an unconditional object operation. Every output-plane operation "+
			"names a precondition: a create names DoesNotExist, and a stat, read or delete names "+
			"the exact generation it is about", ErrPreconditionFailed)
	}
	if conditions.DoesNotExist && conditions.GenerationMatch != 0 {
		return fmt.Errorf("%w: an operation cannot require both that the object is absent and "+
			"that it is at generation %d", ErrPreconditionFailed, conditions.GenerationMatch)
	}
	if conditions.GenerationMatch < 0 || conditions.MetagenerationMatch < 0 {
		return fmt.Errorf("%w: a negative generation or metageneration precondition",
			ErrPreconditionFailed)
	}

	return nil
}

// Attrs is what a store reports about one object.
type Attrs struct {
	Key            string
	Generation     int64
	Metageneration int64
	Size           int64
	Created        time.Time
	Metadata       map[string]string
}

// ListRequest is one page of a bucket-wide listing.
//
// Prefix is here because GCS list permission is bucket-wide and cannot be
// narrowed by IAM: the caller applies the server-derived prefix itself, and
// the honest way to say that is that the request carries one. Nothing outside
// the inventory role ever builds one of these.
//
// After is a KEY, not a page token, and the distinction is the whole reason
// this field is spelled the way it is. A page token is opaque, provider-owned
// and short-lived; the inventory cursor it would have to be stored in is a
// database column that outlives a sweep, a process restart and a leader
// takeover. Resuming from the last key seen is stable across all three, and it
// is what the cursor's own column name already promised.
type ListRequest struct {
	Prefix   string
	PageSize int
	After    string
}

// Page is one listing page.
//
// LastKey is the key of the last object in it, and Done says the listing had
// nothing more. Together they are the next request's After, without either the
// caller or the adapter holding a token.
type Page struct {
	Objects []Attrs
	LastKey string
	Done    bool
}

// Client is the whole surface. A role is given a narrower interface than this;
// this is what an adapter implements.
type Client interface {
	Object(bucket, key string) Handle
	List(ctx context.Context, bucket string, request ListRequest) (Page, error)
}

// Handle is one object, possibly at one generation, possibly under
// preconditions.
type Handle interface {
	If(Conditions) Handle
	Generation(int64) Handle
	NewWriter(ctx context.Context) Writer
	NewReader(ctx context.Context) (io.ReadCloser, error)
	Attrs(ctx context.Context) (Attrs, error)
	Delete(ctx context.Context) error
}

// Writer is one upload.
//
// Abort exists because an upload that failed halfway must be cancelled rather
// than closed: closing commits, and committing half a tree at a key that
// create-if-absent will then refuse forever is the one ambiguity this plane
// cannot recover from cheaply.
type Writer interface {
	io.WriteCloser
	Abort(err error) error
	SetMetadata(metadata map[string]string)
	Attrs() Attrs
}

// Operation is one RPC an adapter issued, for the role-honesty assertion.
//
// It is a *test* observation, not a production one: no production path reads a
// call log, and the recording wrapper lives in hangar/gcstest. The type is
// here because both the wrapper and the assertions need to name it and neither
// should own it.
type Operation string

const (
	OpCreate Operation = "objects.insert"
	OpStat   Operation = "objects.get(metadata)"
	OpRead   Operation = "objects.get(body)"
	OpList   Operation = "objects.list"
	OpDelete Operation = "objects.delete"
)

// Operations is the closed set, so an assertion over "every RPC" can say it
// read them all.
func Operations() []Operation {
	return []Operation{OpCreate, OpStat, OpRead, OpList, OpDelete}
}
