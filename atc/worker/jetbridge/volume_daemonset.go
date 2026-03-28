package jetbridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/atc/compression"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// Compile-time check that DaemonSetVolume satisfies runtime.Volume.
var _ runtime.Volume = (*DaemonSetVolume)(nil)

// DaemonSetVolume represents an artifact stored on a DaemonSet node.
// StreamOut fetches via HTTP from the DaemonSet pod on the source node.
type DaemonSetVolume struct {
	key        string // artifact key (the volume handle)
	handle     string
	workerName string
	dbVolume   db.CreatedVolume
	sourceNode string
	config     Config
	httpClient *http.Client
}

// NewDaemonSetVolume creates a DaemonSetVolume.
func NewDaemonSetVolume(key, handle, workerName string, dbVolume db.CreatedVolume, sourceNode string, config Config) *DaemonSetVolume {
	return &DaemonSetVolume{
		key:        key,
		handle:     handle,
		workerName: workerName,
		dbVolume:   dbVolume,
		sourceNode: sourceNode,
		config:     config,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (v *DaemonSetVolume) Handle() string {
	return v.handle
}

// Key returns the artifact key (the volume handle).
func (v *DaemonSetVolume) Key() string {
	return v.key
}

func (v *DaemonSetVolume) Source() string {
	return v.workerName
}

func (v *DaemonSetVolume) DBVolume() db.CreatedVolume {
	return v.dbVolume
}

// StreamOut fetches the artifact tar from the DaemonSet HTTP server on the
// source node, optionally extracting a sub-path. The response body is a tar
// stream that the caller must close.
func (v *DaemonSetVolume) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	if v.sourceNode == "" {
		return nil, fmt.Errorf("DaemonSetVolume.StreamOut: no source node known (key=%s)", v.key)
	}

	url := v.daemonURL()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = v.httpClient.Do(req)
		if err == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("fetch artifact from %s: %w", url, err)
	}

	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, fmt.Errorf("artifact not found on node %s (key=%s)", v.sourceNode, v.key)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, url)
	}

	return resp.Body, nil
}

func (v *DaemonSetVolume) StreamIn(ctx context.Context, path string, compression compression.Compression, limitInMB float64, reader io.Reader) error {
	return fmt.Errorf("DaemonSetVolume: use hostPath writes (key=%s)", v.key)
}

func (v *DaemonSetVolume) InitializeResourceCache(ctx context.Context, cache db.ResourceCache) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeResourceCache(cache)
}

func (v *DaemonSetVolume) InitializeStreamedResourceCache(ctx context.Context, cache db.ResourceCache, sourceWorkerResourceCacheID int) (*db.UsedWorkerResourceCache, error) {
	if v.dbVolume == nil {
		return nil, nil
	}
	return v.dbVolume.InitializeStreamedResourceCache(cache, sourceWorkerResourceCacheID)
}

func (v *DaemonSetVolume) InitializeTaskCache(ctx context.Context, jobID int, stepName string, path string, privileged bool) error {
	if v.dbVolume == nil {
		return nil
	}
	return v.dbVolume.InitializeTaskCache(jobID, stepName, path)
}

func (v *DaemonSetVolume) daemonURL() string {
	svcName := v.config.ArtifactDaemonService
	if svcName == "" {
		svcName = "artifact-daemon"
	}
	port := v.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}
	return fmt.Sprintf("http://%s.%s.%s.svc.cluster.local:%d/artifacts/%s",
		v.sourceNode, svcName, v.config.Namespace, port, v.key)
}
