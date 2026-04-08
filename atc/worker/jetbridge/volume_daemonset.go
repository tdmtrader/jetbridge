package jetbridge

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
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
	key            string // artifact key (the volume handle)
	handle         string
	workerName     string
	dbVolume       db.CreatedVolume
	sourceNode     string
	sourceIP       string // when set, used directly instead of resolving sourceNode
	config         Config
	httpClient     *http.Client
	nodeIPResolver *NodeIPResolver
	daemonClient   *DaemonClient // for discovering daemon pods when sourceNode is empty
}

// NewDaemonSetVolume creates a DaemonSetVolume.
func NewDaemonSetVolume(key, handle, workerName string, dbVolume db.CreatedVolume, sourceNode string, config Config, nodeIPResolver *NodeIPResolver) *DaemonSetVolume {
	return &DaemonSetVolume{
		key:            key,
		handle:         handle,
		workerName:     workerName,
		dbVolume:       dbVolume,
		sourceNode:     sourceNode,
		config:         config,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		nodeIPResolver: nodeIPResolver,
	}
}

// NewDaemonSetVolumeFromIP creates a DaemonSetVolume with a known daemon pod IP.
// This is used when the daemon IP is already known (e.g., from ProbeResourceCache)
// and no node-name-to-IP resolution is needed.
func NewDaemonSetVolumeFromIP(key, handle, workerName string, daemonIP string, config Config) *DaemonSetVolume {
	return &DaemonSetVolume{
		key:        key,
		handle:     handle,
		workerName: workerName,
		sourceIP:   daemonIP,
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
//
// When enc is non-nil and not RawEncoding, the raw tar from the daemon is
// piped through a compressor before being returned. This satisfies the
// runtime.Artifact contract which requires StreamOut to return a compressed
// stream when compression is requested (e.g., Streamer.StreamFile expects
// gzip-wrapped tar).
func (v *DaemonSetVolume) StreamOut(ctx context.Context, path string, enc compression.Compression) (io.ReadCloser, error) {
	if v.sourceNode == "" && v.sourceIP == "" {
		return nil, fmt.Errorf("DaemonSetVolume.StreamOut: no source node known (key=%s)", v.key)
	}

	url, err := v.daemonURL(ctx)
	if err != nil {
		return nil, err
	}

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

	needsCompression := enc != nil && enc.Encoding() != compression.RawEncoding
	if !needsCompression {
		return resp.Body, nil
	}

	// Pipe the raw tar from the daemon through a compressor so callers
	// (e.g., Streamer.StreamFile) receive the compressed stream they expect.
	pr, pw := io.Pipe()
	go func() {
		compressor := newCompressWriter(pw, enc.Encoding())
		_, copyErr := io.Copy(compressor, resp.Body)
		resp.Body.Close()

		if closeErr := compressor.Close(); closeErr != nil && copyErr == nil {
			copyErr = closeErr
		}
		pw.CloseWithError(copyErr)
	}()

	return pr, nil
}

// SetDaemonClient configures daemon discovery for StreamIn operations.
func (v *DaemonSetVolume) SetDaemonClient(client *DaemonClient) {
	v.daemonClient = client
}

func (v *DaemonSetVolume) StreamIn(ctx context.Context, path string, compression compression.Compression, limitInMB float64, reader io.Reader) error {
	port := v.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}

	var url string
	if v.sourceNode != "" || v.sourceIP != "" {
		u, err := v.daemonURL(ctx)
		if err != nil {
			return fmt.Errorf("DaemonSetVolume.StreamIn: %w", err)
		}
		// Rewrite /artifacts/ to /stream-in/ for tar extraction.
		url = strings.Replace(u, "/artifacts/", "/stream-in/", 1)
	} else if v.daemonClient != nil {
		ips, err := v.daemonClient.daemonIPs(ctx)
		if err != nil {
			return fmt.Errorf("DaemonSetVolume.StreamIn: discover daemons: %w", err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("DaemonSetVolume.StreamIn: no daemon pods discovered")
		}
		url = fmt.Sprintf("http://%s:%d/stream-in/%s", ips[0], port, v.key)
	} else {
		return fmt.Errorf("DaemonSetVolume.StreamIn: no source node or daemon client (key=%s)", v.key)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, reader)
	if err != nil {
		return fmt.Errorf("DaemonSetVolume.StreamIn: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("DaemonSetVolume.StreamIn: PUT %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("DaemonSetVolume.StreamIn: status %d from %s: %s", resp.StatusCode, url, string(body))
	}

	return nil
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

func (v *DaemonSetVolume) daemonURL(ctx context.Context) (string, error) {
	port := v.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}

	// If we already have a direct IP (from ProbeResourceCache), use it.
	if v.sourceIP != "" {
		return fmt.Sprintf("http://%s:%d/artifacts/%s", v.sourceIP, port, v.key), nil
	}

	if v.nodeIPResolver == nil {
		return "", fmt.Errorf("no node IP resolver configured")
	}

	nodeIP, err := v.nodeIPResolver.Resolve(ctx, v.sourceNode)
	if err != nil {
		return "", fmt.Errorf("resolve node IP for %s: %w", v.sourceNode, err)
	}

	return fmt.Sprintf("http://%s:%d/artifacts/%s", nodeIP, port, v.key), nil
}
