package jetbridge

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// Compile-time check that ArtifactStoreVolume satisfies runtime.Volume.
var _ runtime.Volume = (*ArtifactStoreVolume)(nil)

// ArtifactStoreVolume represents an artifact stored on the artifact store PVC.
// It is a lightweight marker type: the pod spec builder uses it to generate
// init containers that extract the artifact from the PVC into emptyDir.
//
// When an executor is configured (via SetExecutor), StreamOut can read file
// contents from the PVC by exec-ing tar commands in the artifact-helper
// sidecar. This enables the ATC to read pipeline configs (set_pipeline),
// variable files (load_var), and task configs (file:) from build artifacts.
type ArtifactStoreVolume struct {
	key        string // e.g. "caches/123.tar" or "artifacts/<handle>.tar"
	handle     string
	workerName string
	dbVolume   db.CreatedVolume

	// executor, podName, and namespace are optional — set via SetExecutor
	// when the ATC needs to stream files from the PVC.
	executor  PodExecutor
	podName   string
	namespace string
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

// SetExecutor configures the PodExecutor, pod name, and namespace needed for
// StreamOut to exec tar commands in the artifact-helper sidecar.
func (v *ArtifactStoreVolume) SetExecutor(executor PodExecutor, podName, namespace string) {
	v.executor = executor
	v.podName = podName
	v.namespace = namespace
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

// StreamOut reads file contents from the artifact tar on the PVC by exec-ing
// in the artifact-helper sidecar. The artifact is stored as a tar file at
// <ArtifactMountPath>/<key>. StreamOut extracts the archive to a temp dir,
// then tars the requested path and streams it back. When compression is
// non-nil and not raw, the output is compressed.
//
// Requires SetExecutor to be called first. Without an executor, returns an error.
func (v *ArtifactStoreVolume) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	if v.executor == nil {
		return nil, fmt.Errorf("ArtifactStoreVolume.StreamOut: no executor configured (key=%s)", v.key)
	}

	archivePath := filepath.Join(ArtifactMountPath, v.key)

	// Build a shell command that extracts the PVC tar to a tmpdir,
	// then re-tars the requested path from there.
	var tarTarget string
	if path == "." || path == "" {
		tarTarget = "."
	} else {
		tarTarget = path
	}

	shellCmd := fmt.Sprintf(
		"tmpdir=$(mktemp -d) && tar xf %s -C $tmpdir && tar cf - -C $tmpdir %s ; rm -rf $tmpdir",
		archivePath, tarTarget,
	)
	cmd := []string{"sh", "-c", shellCmd}

	pr, pw := io.Pipe()

	needsCompression := enc != nil && enc.Encoding() != compression.RawEncoding

	go func() {
		var tarDest io.Writer = pw
		var compressor io.WriteCloser

		if needsCompression {
			compressor = newCompressWriter(pw, enc.Encoding())
			tarDest = compressor
		}

		err := v.executor.ExecInPod(ctx, v.namespace, v.podName,
			artifactHelperContainerName, cmd, nil, tarDest, nil, false,
			ExecAttrs{Purpose: "stream-out", ArtifactKey: ArtifactKey(v.handle)})

		if compressor != nil {
			if closeErr := compressor.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}

		pw.CloseWithError(err)
	}()

	return pr, nil
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
