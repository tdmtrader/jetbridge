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
}
