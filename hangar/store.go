package hangar

import (
	"context"
	"io"
	"time"
)

type TreeAttributes struct {
	Ref          TreeRef   `json:"ref"`
	StoredBytes  int64     `json:"stored_bytes"`
	LogicalBytes int64     `json:"logical_bytes"`
	CreatedAt    time.Time `json:"created_at"`
}

type Store interface {
	EnsureTree(context.Context, Scope, Digest, io.Reader, int64) (TreeAttributes, bool, error)
	InspectTree(context.Context, Scope, Digest, int64) (TreeAttributes, error)
	OpenTree(context.Context, TreeRef, int64) (io.ReadCloser, TreeAttributes, error)
	DeleteTree(context.Context, TreeRef) error
}
