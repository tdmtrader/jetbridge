package jetbridge

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/atc"
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
	wire           *artifactwire.Client
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
		wire:           newWireClient(config),
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
		wire:       newWireClient(config),
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
	// An empty source only fails fast when there is no daemonClient to
	// discover one with. The in-memory ArtifactLocator is wiped by a web
	// restart, so a restart-resume wraps every pre-restart artifact with
	// sourceNode=="" — but the producer node's daemon still has the data on
	// hostPath and answers probes via its registry alias. With a
	// daemonClient configured, fall through to fetchArtifactWithPeerFallback
	// which skips the (unknowable) primary and probes every live daemon.
	// Without this, any in-web StreamFile on such an artifact — e.g. an
	// agent step's file-sourced sidecar config read during resume — errored
	// the build instantly even though the data was one probe away.
	if v.sourceNode == "" && v.sourceIP == "" && v.daemonClient == nil {
		return nil, fmt.Errorf("DaemonSetVolume.StreamOut: no source node known (key=%s)", v.key)
	}

	body, err := v.fetchArtifactWithPeerFallback(ctx)
	if err != nil {
		return nil, err
	}

	needsCompression := enc != nil && enc.Encoding() != compression.RawEncoding
	needsFilter := path != "" && path != "."

	// Fast path: no compression and no sub-path filtering needed.
	if !needsCompression && !needsFilter {
		return body, nil
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
			copyErr = filterTarEntry(body, dest, path)
		} else {
			_, copyErr = io.Copy(dest, body)
		}
		body.Close()

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
	// Stream in lands on the daemon that holds the key, or on any daemon when
	// no source is known yet: it is the same key space as stream out, and the
	// daemon extracts under it.
	var host string
	if v.sourceNode != "" || v.sourceIP != "" {
		h, err := v.daemonHost(ctx)
		if err != nil {
			return fmt.Errorf("DaemonSetVolume.StreamIn: %w", err)
		}
		host = h
	} else if v.daemonClient != nil {
		ips, err := v.daemonClient.daemonIPs(ctx)
		if err != nil {
			return fmt.Errorf("DaemonSetVolume.StreamIn: discover daemons: %w", err)
		}
		if len(ips) == 0 {
			return fmt.Errorf("DaemonSetVolume.StreamIn: no daemon pods discovered")
		}
		host = ips[0]
	} else {
		return fmt.Errorf("DaemonSetVolume.StreamIn: no source node or daemon client (key=%s)", v.key)
	}

	// The wire client's streaming path carries the mTLS client cert when TLS
	// is enabled and has no whole-request timeout, so large uploads are not
	// severed mid-body (the handshake is still bounded by its transport's
	// ResponseHeaderTimeout).
	if err := v.wire.StreamIn(ctx, host, v.key, reader); err != nil {
		return fmt.Errorf("DaemonSetVolume.StreamIn: %w", err)
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

func (v *DaemonSetVolume) InitializeTaskCache(ctx context.Context, identity atc.TaskCacheIdentity, stepName string, path string, privileged bool) error {
	if v.dbVolume == nil {
		return nil
	}
	return v.dbVolume.InitializeTaskCache(identity, stepName, path)
}

// fetchArtifactWithPeerFallback gets the artifact tar from the recorded
// source node, falling back to a peer daemon when the recorded node is
// unreachable, has been removed from the cluster, refuses (4xx/5xx), or was
// never recorded at all (sourceNode=="" after a web restart wiped the
// locator). Peer fallback only fires when a daemonClient is configured;
// otherwise the recorded-source error is surfaced verbatim (preserves
// existing behavior for tests / callers without daemon discovery).
//
// The fallback path probes every live daemon for a step copy of the
// artifact, then streams out from the first daemon that has it. The caller
// is responsible for closing the returned body.
//
// On a probe miss (no peer has the data), returns a "not found on node
// or any peer" error so debug output makes the failure mode obvious.
func (v *DaemonSetVolume) fetchArtifactWithPeerFallback(ctx context.Context) (io.ReadCloser, error) {
	primaryHost, primaryHostErr := v.daemonHost(ctx)

	// Try the recorded source first (skip if the host is unknown, e.g. the
	// NodeIPResolver returned ErrNodeNameIsIP).
	var fetchErr error
	if primaryHostErr == nil {
		body, err := v.fetchOnce(ctx, primaryHost, v.key)
		if err == nil {
			return body, nil
		}
		fetchErr = err
	}

	// No fallback configured — surface the recorded-source error verbatim.
	if v.daemonClient == nil {
		if primaryHostErr != nil {
			return nil, primaryHostErr
		}
		if errors.Is(fetchErr, artifactwire.ErrNotFound) {
			return nil, fmt.Errorf("artifact not found on node %s (key=%s)", v.sourceNode, v.key)
		}
		var refusal *artifactwire.Refusal
		if errors.As(fetchErr, &refusal) {
			// The refusal carries the daemon's own account, which is what a
			// reader needs: a 400 that says "certificate" is a different
			// fix from a 400 that says "invalid key".
			return nil, fmt.Errorf("unexpected status %d: %w", refusal.Status, fetchErr)
		}
		return nil, fmt.Errorf("fetch artifact: %w", fetchErr)
	}

	// Recorded source failed in some way (transport error, ErrNodeNameIsIP,
	// 4xx, or 5xx). Probe peers and try again.
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	peerIP, found, _ := v.daemonClient.ProbeStepArtifact(probeCtx, v.key)
	if !found {
		if v.sourceNode == "" {
			return nil, fmt.Errorf("no source node known and artifact not found on any daemon (key=%s)", v.key)
		}
		return nil, fmt.Errorf("artifact not found on node %s or any peer (key=%s)", v.sourceNode, v.key)
	}

	// Peers hold mirrored bytes under the steps/ path, not under a
	// producer-side registry alias: mirrored data arrives via stream in,
	// which extracts to {storage}/steps/{key}. The producer served the key
	// by alias resolution; for a peer the key must name the on-disk path.
	body, err := v.fetchOnce(ctx, peerIP, artifactwire.StepsPrefix+v.key)
	if err != nil {
		return nil, fmt.Errorf("fetch artifact from peer %s: %w", peerIP, err)
	}
	return body, nil
}

// fetchOnce streams key out of the daemon at host with the existing
// 3-attempt / 2-second-backoff retry policy over TRANSPORT failures. A
// refusal is the daemon's answer and is returned as is: retrying a 404 asks
// the same disk the same question.
func (v *DaemonSetVolume) fetchOnce(ctx context.Context, host, key string) (io.ReadCloser, error) {
	var (
		body io.ReadCloser
		err  error
	)
	for attempt := 0; attempt < 3; attempt++ {
		body, err = v.wire.StreamOut(ctx, host, key)
		if err == nil {
			return body, nil
		}
		var refusal *artifactwire.Refusal
		if errors.As(err, &refusal) {
			return nil, err
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}
	return nil, err
}

// daemonHost is the daemon that holds this volume's key: the recorded pod IP
// when one is known, else the node the artifact was recorded on, resolved.
func (v *DaemonSetVolume) daemonHost(ctx context.Context) (string, error) {
	// If we already have a direct IP (from ProbeResourceCache), use it.
	if v.sourceIP != "" {
		return v.sourceIP, nil
	}

	// No recorded source node (e.g. the locator was wiped by a web restart
	// before this volume was wrapped). Error here rather than asking the
	// NodeIPResolver to resolve "" — fetchArtifactWithPeerFallback treats
	// this as a failed primary and falls back to probing live daemons.
	if v.sourceNode == "" {
		return "", fmt.Errorf("no source node recorded (key=%s)", v.key)
	}

	if v.nodeIPResolver == nil {
		return "", fmt.Errorf("no node IP resolver configured")
	}

	nodeIP, err := v.nodeIPResolver.Resolve(ctx, v.sourceNode)
	if err != nil {
		return "", fmt.Errorf("resolve node IP for %s: %w", v.sourceNode, err)
	}

	return nodeIP, nil
}
