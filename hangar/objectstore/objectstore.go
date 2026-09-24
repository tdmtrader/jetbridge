// Package objectstore defines immutable object operations shared by Hangar backends.
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

	// ErrBucketNotFound is the BUCKET being absent, and it deliberately does
	// not wrap ErrNotFound.
	//
	// It used to. A deleted bucket, or a controller started against a bucket
	// name nobody publishes into, made every operation answer "not found" --
	// and the reclaim path reads object absence as evidence that the object was
	// removed. Folded together, a wrong bucket finalized the entire registered
	// set as this plane's own successful deletions, with no violation and no
	// at-risk, while every object was still there. Deletion truth is the one
	// thing this plane must never get wrong, so the two answers are two errors.
	//
	// It is NOT a claim that every bucket-absence is detectable. Measured
	// against both tiers: a bucket-wide LIST in a missing bucket answers
	// storage.ErrBucketNotExist and reaches this sentinel; an OBJECT stat or
	// delete in a missing bucket answers an ordinary object 404, because the
	// JSON API says the same thing for both and the SDK cannot tell either.
	// What decides the reclaim case is therefore the job's own admitted-attempt
	// history on the control plane, not this error.
	ErrBucketNotFound = errors.New("hangar/objectstore: bucket not found")
)

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

	// AfterGeneration is the second half of the lexicographic (key, generation)
	// after-key Req 43 makes the durable cursor out of.
	//
	// A listing resumed from a key alone cannot tell "I already did this
	// object" from "this key was recreated while I was away": the second is a
	// NEW object at an old name, and dropping it would mean an object the
	// deployment created is never swept in the cycle it appeared in. So After
	// is resumed from inclusively and an object at exactly that key is dropped
	// only when its generation is no newer than this. Zero keeps the older
	// meaning -- drop the resumed-from key outright -- so a caller that has no
	// generation to name is not silently given a different listing.
	AfterGeneration int64
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

// Client exposes immutable creation, inspection and reading. Deletion is a
// separate capability so publisher and inventory clients cannot remove data.
type Client interface {
	CreateAbsent(ctx context.Context, bucket, key string, metadata map[string]string, body io.Reader) (Attrs, error)
	StatCurrent(ctx context.Context, bucket, key string) (Attrs, error)
	StatExact(ctx context.Context, bucket, key string, generation int64) (Attrs, error)
	OpenExact(ctx context.Context, bucket, key string, generation int64) (io.ReadCloser, error)
	List(ctx context.Context, bucket string, request ListRequest) (Page, error)
}

// DeleteClient can inspect and delete exact generations, but cannot read bodies
// or publish objects. DeleteExact must never remove a replacement generation.
type DeleteClient interface {
	StatExact(ctx context.Context, bucket, key string, generation int64) (Attrs, error)
	DeleteExact(ctx context.Context, bucket, key string, generation int64) error
}

// ValidateGeneration rejects accidental current-object operations on exact paths.
func ValidateGeneration(generation int64) error {
	if generation <= 0 {
		return fmt.Errorf("%w: exact generation must be positive", ErrPreconditionFailed)
	}
	return nil
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
