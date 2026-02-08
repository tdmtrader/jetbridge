package k8sruntime

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
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

func (v *Volume) Handle() string {
	return v.dbVolume.Handle()
}

func (v *Volume) Source() string {
	return v.dbVolume.WorkerName()
}

func (v *Volume) DBVolume() db.CreatedVolume {
	return v.dbVolume
}

// StreamIn copies data into the Pod by exec-ing `tar xf -` at the target path.
func (v *Volume) StreamIn(ctx context.Context, path string, _ compression.Compression, limitInMB float64, reader io.Reader) error {
	targetPath := v.resolvedPath(path)

	cmd := []string{"tar", "xf", "-", "-C", targetPath}

	err := v.executor.ExecInPod(ctx, v.namespace, v.podName, v.containerName, cmd, reader, nil, nil)
	if err != nil {
		return fmt.Errorf("stream in via exec: %w", err)
	}

	return nil
}

// StreamOut extracts data from the Pod by exec-ing `tar cf -` at the target path.
func (v *Volume) StreamOut(ctx context.Context, path string, _ compression.Compression) (io.ReadCloser, error) {
	targetPath := v.resolvedPath(path)

	cmd := []string{"tar", "cf", "-", "-C", targetPath, "."}

	var stdout bytes.Buffer
	err := v.executor.ExecInPod(ctx, v.namespace, v.podName, v.containerName, cmd, nil, &stdout, nil)
	if err != nil {
		return nil, fmt.Errorf("stream out via exec: %w", err)
	}

	return io.NopCloser(&stdout), nil
}

func (v *Volume) resolvedPath(path string) string {
	if path == "." || path == "" {
		return v.mountPath
	}
	return filepath.Join(v.mountPath, path)
}

func (v *Volume) InitializeResourceCache(ctx context.Context, urc db.ResourceCache) (*db.UsedWorkerResourceCache, error) {
	return nil, nil
}

func (v *Volume) InitializeStreamedResourceCache(ctx context.Context, urc db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) {
	return nil, nil
}

func (v *Volume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error {
	return nil
}
