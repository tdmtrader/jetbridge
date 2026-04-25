package jetbridge

import (
	"archive/tar"
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

	resp, err := v.fetchArtifactWithPeerFallback(ctx)
	if err != nil {
		return nil, err
	}

	needsCompression := enc != nil && enc.Encoding() != compression.RawEncoding
	needsFilter := path != "" && path != "."

	// Fast path: no compression and no sub-path filtering needed.
	if !needsCompression && !needsFilter {
		return resp.Body, nil
	}

	// Pipe the daemon's raw tar through optional sub-path filtering and
	// optional compression. This satisfies the runtime.Artifact contract:
	// - Volume.StreamOut with a sub-path produces a tar containing only that
	//   entry (matching `tar cf - -C /mount path` semantics).
	// - Volume.StreamOut with compression wraps the tar in a compressor.
	pr, pw := io.Pipe()
	go func() {
		var dest io.Writer = pw
		var compressor io.WriteCloser

		if needsCompression {
			compressor = newCompressWriter(pw, enc.Encoding())
			dest = compressor
		}

		var copyErr error
		if needsFilter {
			copyErr = filterTarEntry(resp.Body, dest, path)
		} else {
			_, copyErr = io.Copy(dest, resp.Body)
		}
		resp.Body.Close()

		if compressor != nil {
			if closeErr := compressor.Close(); closeErr != nil && copyErr == nil {
				copyErr = closeErr
			}
		}
		pw.CloseWithError(copyErr)
	}()

	return pr, nil
}

// filterTarEntry reads a tar stream from src and writes a new tar stream to
// dst containing only the entry matching targetPath. This emulates the
// behavior of `tar cf - -C /mount <path>` which the regular Volume.StreamOut
// uses when a sub-path is requested.
func filterTarEntry(src io.Reader, dst io.Writer, targetPath string) error {
	tr := tar.NewReader(src)
	tw := tar.NewWriter(dst)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			tw.Close()
			return fmt.Errorf("reading tar for filter: %w", err)
		}

		if hdr.Name != targetPath {
			continue
		}

		if err := tw.WriteHeader(hdr); err != nil {
			tw.Close()
			return fmt.Errorf("writing filtered tar header: %w", err)
		}
		if _, err := io.Copy(tw, tr); err != nil {
			tw.Close()
			return fmt.Errorf("writing filtered tar body: %w", err)
		}
		// Only include the first match — tar entries are unique.
		break
	}

	return tw.Close()
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

// fetchArtifactWithPeerFallback gets the artifact tar from the recorded
// source node, falling back to a peer daemon when the recorded node is
// unreachable, has been removed from the cluster, or returns an error
// (4xx/5xx). Peer fallback only fires when a daemonClient is configured;
// otherwise the recorded-source error is surfaced verbatim (preserves
// existing behavior for tests / callers without daemon discovery).
//
// The fallback path probes every live daemon for a step copy of the
// artifact (HEAD /artifacts/steps/{key}), then fetches from the first
// daemon that has it. A successful peer fetch returns a 200 response
// whose body the caller is responsible for closing.
//
// On a probe miss (no peer has the data), returns a "not found on node
// or any peer" error so debug output makes the failure mode obvious.
func (v *DaemonSetVolume) fetchArtifactWithPeerFallback(ctx context.Context) (*http.Response, error) {
	primaryURL, primaryURLErr := v.daemonURL(ctx)

	// Try the recorded source first (skip if URL resolution failed —
	// e.g., NodeIPResolver returned ErrNodeNameIsIP).
	var (
		resp        *http.Response
		fetchErr    error
		recordedURL string
	)
	if primaryURLErr == nil {
		recordedURL = primaryURL
		resp, fetchErr = v.fetchOnce(ctx, primaryURL)
		if fetchErr == nil && resp.StatusCode == http.StatusOK {
			return resp, nil
		}
	}

	// No fallback configured — surface the recorded-source error verbatim.
	if v.daemonClient == nil {
		if primaryURLErr != nil {
			return nil, primaryURLErr
		}
		if resp != nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return nil, fmt.Errorf("artifact not found on node %s (key=%s)", v.sourceNode, v.key)
			}
			return nil, fmt.Errorf("unexpected status %d from %s", resp.StatusCode, recordedURL)
		}
		return nil, fmt.Errorf("fetch artifact from %s: %w", recordedURL, fetchErr)
	}

	// Recorded source failed in some way (transport error, ErrNodeNameIsIP,
	// 4xx, or 5xx). Probe peers and try again.
	if resp != nil {
		resp.Body.Close()
	}

	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	peerIP, found, _ := v.daemonClient.ProbeStepArtifact(probeCtx, v.key)
	if !found {
		return nil, fmt.Errorf("artifact not found on node %s or any peer (key=%s)", v.sourceNode, v.key)
	}

	port := v.config.ArtifactDaemonPort
	if port == 0 {
		port = 7780
	}
	peerURL := fmt.Sprintf("http://%s:%d/artifacts/%s", peerIP, port, v.key)
	resp, err := v.fetchOnce(ctx, peerURL)
	if err != nil {
		return nil, fmt.Errorf("fetch artifact from peer %s: %w", peerIP, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("peer %s returned status %d (key=%s)", peerIP, resp.StatusCode, v.key)
	}
	return resp, nil
}

// fetchOnce performs a single GET attempt loop with the existing 3-attempt /
// 2-second-backoff retry policy. Returns the response on success or the
// last transport error after all retries fail.
func (v *DaemonSetVolume) fetchOnce(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	var resp *http.Response
	for attempt := 0; attempt < 3; attempt++ {
		resp, err = v.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return nil, err
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
