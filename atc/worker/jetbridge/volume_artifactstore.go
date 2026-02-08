package jetbridge

import (
	"context"
	"fmt"
	"io"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// Compile-time check that ArtifactStoreVolume satisfies runtime.Volume.
var _ runtime.Volume = (*ArtifactStoreVolume)(nil)

// ArtifactStoreVolume represents an artifact stored on the artifact store PVC.
// It is a lightweight marker type: the pod spec builder uses it to generate
// init containers that extract the artifact from the PVC into emptyDir.
// StreamIn/StreamOut are not used -- data movement is handled by init
// containers and the artifact-helper sidecar.
type ArtifactStoreVolume struct {
	key        string // e.g. "caches/123.tar" or "artifacts/<handle>.tar"
	handle     string
	workerName string
	dbVolume   db.CreatedVolume
}

// NewArtifactStoreVolume creates an ArtifactStoreVolume with the given key
// (relative path on the artifact PVC), handle, worker name, and optional
// DB volume for cache initialization.
func NewArtifactStoreVolume(key, handle, workerName string, dbVolume db.CreatedVolume) *ArtifactStoreVolume {
	return &ArtifactStoreVolume{
		key:        key,
		handle:     handle,
		workerName: workerName,
		dbVolume:   dbVolume,
	}
}

func (v *ArtifactStoreVolume) Handle() string {
	return v.handle
}

func (v *ArtifactStoreVolume) Source() string {
	return v.workerName
}

func (v *ArtifactStoreVolume) DBVolume() db.CreatedVolume {
	return v.dbVolume
}

// Key returns the relative path on the artifact PVC where this artifact
// is stored (e.g. "caches/123.tar").
func (v *ArtifactStoreVolume) Key() string {
	return v.key
}

// StreamOut is not used for ArtifactStoreVolume -- init containers handle
// data extraction from the PVC.
func (v *ArtifactStoreVolume) StreamOut(ctx context.Context, path string, compression compression.Compression) (io.ReadCloser, error) {
	return nil, fmt.Errorf("ArtifactStoreVolume: use init containers (key=%s)", v.key)
}

// StreamIn is not used for ArtifactStoreVolume -- the artifact-helper
// sidecar handles data upload to the PVC.
func (v *ArtifactStoreVolume) StreamIn(ctx context.Context, path string, compression compression.Compression, limitInMB float64, reader io.Reader) error {
	return fmt.Errorf("ArtifactStoreVolume: use artifact-helper (key=%s)", v.key)
}

func (v *ArtifactStoreVolume) InitializeResourceCache(ctx context.Context, cache db.ResourceCache) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeResourceCache(cache)
}

func (v *ArtifactStoreVolume) InitializeStreamedResourceCache(ctx context.Context, cache db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeStreamedResourceCache(cache, sourceWorkerResourceCacheID)
}

func (v *ArtifactStoreVolume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error {
	if v.dbVolume == nil {
		return nil
	}
	return v.dbVolume.InitializeTaskCache(jobID, stepName, path)
}
