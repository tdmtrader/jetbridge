// Package hangar defines durable, immutable storage contracts for agentic
// workflow artifacts.
package hangar

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrNotFound = errors.New("hangar: object not found")
	ErrCorrupt  = errors.New("hangar: object corrupt")
	ErrConflict = errors.New("hangar: immutable object conflict")

	// ErrUnrepairable marks an object whose stored bytes are intact but whose
	// metadata cannot be restored without inventing a value. It is deliberately
	// distinct from ErrCorrupt: the content may be perfectly good, yet this
	// build has no proof for every key the read contract demands, so refusing
	// is the only honest answer.
	ErrUnrepairable = errors.New("hangar: object metadata is not derivable")
)

type Kind string

const (
	KindSnapshot   Kind = "snapshots"
	KindCheckpoint Kind = "checkpoints"
)

type Digest string

type ObjectRef struct {
	Kind       Kind
	Digest     Digest
	Key        string
	Generation int64
}

type Attributes struct {
	Ref               ObjectRef
	CompressedBytes   int64
	UncompressedBytes int64
	CreatedAt         time.Time
}

type Store interface {
	Ensure(context.Context, Kind, Digest, io.Reader, int64) (Attributes, error)
	Inspect(context.Context, Kind, Digest, int64) (Attributes, error)
	Open(context.Context, ObjectRef, int64) (io.ReadCloser, Attributes, error)
	Delete(context.Context, ObjectRef) error

	// RepairDerivableMetadata restores the exact stored metadata vocabulary of
	// an object that kept its bytes but lost its custom metadata. It is the
	// only sanctioned write to a committed object, and it is permitted solely
	// because every key is derivable from the object key plus the stored bytes
	// and because the bytes must prove themselves against the key's digest
	// first. Reads stay fail-closed; this never relaxes them.
	RepairDerivableMetadata(context.Context, Kind, Digest, int64) (Attributes, error)
}
