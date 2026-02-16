package jetbridge

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"
)

// Compile-time check that Volume satisfies runtime.Volume.
var _ runtime.Volume = (*Volume)(nil)

// PodExecutor abstracts exec-ing commands inside a Kubernetes Pod container.
// This allows unit tests to inject a fake without needing a real K8s API server.
type PodExecutor interface {
	ExecInPod(
		ctx context.Context,
		namespace, podName, containerName string,
		command []string,
		stdin io.Reader,
		stdout, stderr io.Writer,
		tty bool,
	) error
}

// ExecExitError is returned by PodExecutor when the executed process exits
// with a non-zero status. The ExitCode field holds the process exit code.
type ExecExitError struct {
	ExitCode int
}

func (e *ExecExitError) Error() string {
	return fmt.Sprintf("process exited with code %d", e.ExitCode)
}

// Volume implements runtime.Volume backed by a path inside a Kubernetes Pod.
// Data is streamed in/out by exec-ing tar inside the Pod container.
type Volume struct {
	dbVolume      db.CreatedVolume
	handle        string
	workerName    string
	executor      PodExecutor
	podName       string
	namespace     string
	containerName string
	mountPath     string
}

// NewVolume creates a Volume backed by a path inside a Kubernetes Pod.
func NewVolume(
	dbVolume db.CreatedVolume,
	executor PodExecutor,
	podName, namespace, containerName, mountPath string,
) *Volume {
	return &Volume{
		dbVolume:      dbVolume,
		executor:      executor,
		podName:       podName,
		namespace:     namespace,
		containerName: containerName,
		mountPath:     mountPath,
	}
}

// NewStubVolume creates a Volume that acts as a placeholder for resource
// cache tracking. It does not require a db.CreatedVolume since K8s volumes
// are ephemeral emptyDirs managed by the Pod lifecycle.
func NewStubVolume(handle, workerName, mountPath string) *Volume {
	return &Volume{
		handle:     handle,
		workerName: workerName,
		mountPath:  mountPath,
	}
}

// NewDeferredVolume creates a Volume with an executor but without a pod name.
// The pod name is set later via SetPodName when the pod is created in
// Container.Run(). This supports the pattern where FindOrCreateContainer
// creates volumes before the pod exists.
func NewDeferredVolume(handle, workerName string, executor PodExecutor, namespace, containerName, mountPath string) *Volume {
	return &Volume{
		handle:        handle,
		workerName:    workerName,
		executor:      executor,
		namespace:     namespace,
		containerName: containerName,
		mountPath:     mountPath,
	}
}

// NewCacheVolume creates a Volume backed by a subdirectory on the cache PVC.
// The mountPath is automatically set to CacheBasePath/<handle>, so StreamIn/
// StreamOut target the PVC subdirectory. The dbVolume parameter may be nil
// when the volume hasn't been registered in the DB yet.
func NewCacheVolume(dbVolume db.CreatedVolume, handle, workerName string, executor PodExecutor, namespace, containerName string) *Volume {
	return &Volume{
		dbVolume:      dbVolume,
		handle:        handle,
		workerName:    workerName,
		executor:      executor,
		namespace:     namespace,
		containerName: containerName,
		mountPath:     filepath.Join(CacheBasePath, handle),
	}
}

// SetPodName sets the pod name on a deferred volume. This is called when
// the pod is created in Container.Run(), enabling StreamIn/StreamOut.
func (v *Volume) SetPodName(podName string) {
	v.podName = podName
}

// PodName returns the pod name this volume is bound to, or empty if not yet set.
func (v *Volume) PodName() string {
	return v.podName
}

// MountPath returns the path where this volume is mounted in the container.
func (v *Volume) MountPath() string {
	return v.mountPath
}

// HasExecutor reports whether this Volume has a PodExecutor configured,
// meaning StreamIn/StreamOut can function. Stub volumes without an executor
// cannot perform data streaming.
func (v *Volume) HasExecutor() bool {
	return v.executor != nil
}

func (v *Volume) Handle() string {
	if v.dbVolume != nil {
		return v.dbVolume.Handle()
	}
	return v.handle
}

func (v *Volume) Source() string {
	if v.dbVolume != nil {
		return v.dbVolume.WorkerName()
	}
	return v.workerName
}

func (v *Volume) DBVolume() db.CreatedVolume {
	return v.dbVolume
}

// StreamIn copies data into the Pod by exec-ing `tar xf -` at the target path.
// When compression is non-nil and not raw, the incoming stream is decompressed
// before being piped to tar.
func (v *Volume) StreamIn(ctx context.Context, path string, enc compression.Compression, limitInMB float64, reader io.Reader) error {
	logger := lagerctx.FromContext(ctx).Session("volume-stream-in", lager.Data{
		"pod":        v.podName,
		"mount-path": v.mountPath,
		"path":       path,
	})

	ctx, span := tracing.StartSpan(ctx, "k8s.volume.stream-in", tracing.Attrs{
		"pod-name": v.podName,
		"path":     v.resolvedPath(path),
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	targetPath := v.resolvedPath(path)

	// If the caller passed a compression encoding, decompress the stream
	// before feeding it to tar. The Streamer sends compressed tar streams
	// and expects StreamIn to decompress them.
	actualReader := reader
	if enc != nil && enc.Encoding() != compression.RawEncoding {
		rc, ok := reader.(io.ReadCloser)
		if !ok {
			rc = io.NopCloser(reader)
		}
		decompressed, err := enc.NewReader(rc)
		if err != nil {
			spanErr = err
			return fmt.Errorf("stream in: create decompressor: %w", err)
		}
		defer decompressed.Close()
		actualReader = decompressed
	}

	cmd := []string{"tar", "xf", "-", "-C", targetPath}

	err := v.executor.ExecInPod(ctx, v.namespace, v.podName, v.containerName, cmd, actualReader, nil, nil, false)
	if err != nil {
		logger.Error("failed-to-stream-in", err)
		spanErr = err
		return fmt.Errorf("stream in via exec: %w", err)
	}

	return nil
}

// StreamOut extracts data from the Pod by exec-ing `tar cf -` at the target path.
// When compression is non-nil and not raw, the tar stream is compressed before
// being returned to the caller. This satisfies the runtime.Artifact contract
// which requires StreamOut to return a compressed tar stream.
func (v *Volume) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	logger := lagerctx.FromContext(ctx).Session("volume-stream-out", lager.Data{
		"pod":        v.podName,
		"mount-path": v.mountPath,
		"path":       path,
	})

	ctx, span := tracing.StartSpan(ctx, "k8s.volume.stream-out", tracing.Attrs{
		"pod-name": v.podName,
		"path":     v.resolvedPath(path),
	})

	// For root path ("."/empty), tar the entire mount directory.
	// For sub-paths, tar relative to the mount root so that both files
	// and directories work correctly (tar cf - -C /mount file.yml).
	var cmd []string
	if path == "." || path == "" {
		cmd = []string{"tar", "cf", "-", "-C", v.mountPath, "."}
	} else {
		cmd = []string{"tar", "cf", "-", "-C", v.mountPath, path}
	}

	pr, pw := io.Pipe()

	needsCompression := enc != nil && enc.Encoding() != compression.RawEncoding

	go func() {
		var tarDest io.Writer = pw
		var compressor io.WriteCloser

		if needsCompression {
			compressor = newCompressWriter(pw, enc.Encoding())
			tarDest = compressor
		}

		err := v.executor.ExecInPod(ctx, v.namespace, v.podName, v.containerName, cmd, nil, tarDest, nil, false)
		if err != nil {
			logger.Error("failed-to-stream-out", err)
		}

		// Close the compressor first to flush any buffered data and write
		// the compression footer, then close the pipe.
		if compressor != nil {
			if closeErr := compressor.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
		}

		// End the span in the goroutine that owns it to avoid a data race
		// with the caller's deferred span cleanup.
		tracing.End(span, err)
		pw.CloseWithError(err)
	}()

	return pr, nil
}

// newCompressWriter creates a compressing io.WriteCloser for the given encoding.
func newCompressWriter(w io.Writer, encoding compression.Encoding) io.WriteCloser {
	switch encoding {
	case compression.ZstdEncoding:
		enc, err := zstd.NewWriter(w)
		if err != nil {
			// zstd.NewWriter only errors on invalid options; safe to panic.
			panic(fmt.Sprintf("zstd.NewWriter: %v", err))
		}
		return enc
	case compression.S2Encoding:
		return s2.NewWriter(w)
	default:
		// Default to gzip for GzipEncoding and any unknown encoding.
		return gzip.NewWriter(w)
	}
}

func (v *Volume) resolvedPath(path string) string {
	if path == "." || path == "" {
		return v.mountPath
	}
	return filepath.Join(v.mountPath, path)
}

func (v *Volume) InitializeResourceCache(ctx context.Context, cache db.ResourceCache) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeResourceCache(cache)
}

func (v *Volume) InitializeStreamedResourceCache(ctx context.Context, cache db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeStreamedResourceCache(cache, sourceWorkerResourceCacheID)
}

func (v *Volume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error {
	if v.dbVolume == nil {
		return nil
	}
	return v.dbVolume.InitializeTaskCache(jobID, stepName, path)
}
