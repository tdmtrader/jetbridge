package jetbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
)

const SnapshotDaemonDriver = "jetbridge-daemon-v1"

type SnapshotLocationResolver interface {
	LocationsForDigest(context.Context, snapshot.Digest) ([]snapshot.Location, error)
}

// SnapshotContentStore stores canonical archives in the daemon's immutable
// digest namespace. Physical replica identity is the stable Kubernetes node
// name; EndpointSlice addresses are resolved anew for every operation.
type SnapshotContentStore struct {
	daemon            *DaemonClient
	locations         SnapshotLocationResolver
	replicationFactor int
	maxBytes          int64
	archiveLimits     snapshot.ArchiveLimits
}

var _ snapshot.ContentStore = (*SnapshotContentStore)(nil)

func NewSnapshotContentStore(
	daemon *DaemonClient,
	locations SnapshotLocationResolver,
	replicationFactor int,
	archiveLimits snapshot.ArchiveLimits,
) (*SnapshotContentStore, error) {
	if daemon == nil || locations == nil {
		return nil, fmt.Errorf("snapshot content store requires daemon client and location resolver")
	}
	if _, err := daemon.snapshotHTTPClient(); err != nil {
		return nil, fmt.Errorf("snapshot daemon transport: %w", err)
	}
	if replicationFactor <= 0 {
		return nil, fmt.Errorf("snapshot replication factor must be positive")
	}
	maxBytes, err := archiveLimits.CanonicalArchiveByteLimit()
	if err != nil {
		return nil, fmt.Errorf("snapshot archive limits: %w", err)
	}
	return &SnapshotContentStore{
		daemon:            daemon,
		locations:         locations,
		replicationFactor: replicationFactor,
		maxBytes:          maxBytes,
		archiveLimits:     archiveLimits,
	}, nil
}

func snapshotKeyForDigest(digest snapshot.Digest) string {
	return "snapshots/sha256/" + strings.TrimPrefix(digest.String(), "sha256:") + ".tar"
}

func (store *SnapshotContentStore) Put(ctx context.Context, digest snapshot.Digest, source io.Reader) ([]snapshot.Location, error) {
	if err := digest.Validate(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("snapshot content source is required")
	}
	spool, size, actualDigest, err := spoolSnapshot(ctx, source, store.maxBytes)
	if err != nil {
		return nil, err
	}
	spoolName := spool.Name()
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		spool.Close()
		os.Remove(spoolName)
		return nil, err
	}
	if err := snapshot.ValidateArchiveLimits(ctx, spool, store.archiveLimits); err != nil {
		spool.Close()
		os.Remove(spoolName)
		return nil, err
	}
	if err := spool.Close(); err != nil {
		os.Remove(spoolName)
		return nil, err
	}
	defer os.Remove(spoolName)
	if actualDigest != digest {
		return nil, fmt.Errorf("snapshot upload digest mismatch: got %s, want %s", actualDigest, digest)
	}

	endpoints, err := store.daemon.DaemonEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no identified artifact daemon endpoints available")
	}
	if len(endpoints) > store.replicationFactor {
		endpoints = endpoints[:store.replicationFactor]
	}

	type uploadResult struct {
		location snapshot.Location
		err      error
	}
	results := make(chan uploadResult, len(endpoints))
	var wait sync.WaitGroup
	for _, endpoint := range endpoints {
		endpoint := endpoint
		wait.Add(1)
		go func() {
			defer wait.Done()
			location, err := store.putEndpoint(ctx, endpoint, digest, spoolName, size)
			results <- uploadResult{location: location, err: err}
		}()
	}
	wait.Wait()
	close(results)

	locations := make([]snapshot.Location, 0, len(endpoints))
	errorsByNode := make([]error, 0, len(endpoints))
	for result := range results {
		if result.err != nil {
			errorsByNode = append(errorsByNode, result.err)
			continue
		}
		locations = append(locations, result.location)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i].Node < locations[j].Node })
	if len(locations) == 0 {
		return nil, fmt.Errorf("snapshot upload received no replica acknowledgements: %w", errors.Join(errorsByNode...))
	}
	// Partial acknowledgement is deliberate degraded success. The durable
	// location rows let lifecycle repair restore the configured factor later.
	return locations, nil
}

func spoolSnapshot(ctx context.Context, source io.Reader, maxBytes int64) (*os.File, int64, snapshot.Digest, error) {
	spool, err := os.CreateTemp("", ".jetbridge-snapshot-*")
	if err != nil {
		return nil, 0, "", err
	}
	failed := true
	defer func() {
		if failed {
			spool.Close()
			os.Remove(spool.Name())
		}
	}()
	hasher := sha256.New()
	buffer := make([]byte, 128*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, size, "", err
		}
		n, readErr := source.Read(buffer)
		if n > 0 {
			if size+int64(n) > maxBytes {
				return nil, size, "", fmt.Errorf("snapshot upload exceeds maximum of %d bytes", maxBytes)
			}
			written, writeErr := io.MultiWriter(spool, hasher).Write(buffer[:n])
			size += int64(written)
			if writeErr != nil {
				return nil, size, "", writeErr
			}
			if written != n {
				return nil, size, "", io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, size, "", readErr
		}
	}
	if err := spool.Sync(); err != nil {
		return nil, size, "", err
	}
	actual := snapshot.Digest("sha256:" + hex.EncodeToString(hasher.Sum(nil)))
	failed = false
	return spool, size, actual, nil
}

func (store *SnapshotContentStore) putEndpoint(
	ctx context.Context,
	endpoint DaemonEndpoint,
	digest snapshot.Digest,
	spoolName string,
	size int64,
) (snapshot.Location, error) {
	file, err := os.Open(spoolName)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: %w", endpoint.NodeName, err)
	}
	defer file.Close()
	key := snapshotKeyForDigest(digest)
	target, err := store.daemon.snapshotURL(endpoint, key)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: %w", endpoint.NodeName, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), file)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: %w", endpoint.NodeName, err)
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", "application/x-tar")
	client, err := store.daemon.snapshotHTTPClient()
	if err != nil {
		return snapshot.Location{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: %w", endpoint.NodeName, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: daemon status %d", endpoint.NodeName, response.StatusCode)
	}
	return snapshot.Location{Digest: digest, Driver: SnapshotDaemonDriver, Key: key, Node: endpoint.NodeName}, nil
}

func (store *SnapshotContentStore) Open(ctx context.Context, value snapshot.Snapshot) (io.ReadCloser, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	if value.ContentState != snapshot.ContentStateAvailable {
		return nil, fmt.Errorf("snapshot content is not available")
	}
	recorded, err := store.locations.LocationsForDigest(ctx, value.Digest)
	if err != nil {
		return nil, fmt.Errorf("resolve recorded snapshot locations: %w", err)
	}
	live, err := store.daemon.DaemonEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	ordered := orderedSnapshotEndpoints(recorded, live, value.Digest)
	if len(ordered) == 0 {
		return nil, fmt.Errorf("no live artifact daemon endpoints for snapshot %s", value.Digest)
	}

	errorsByNode := make([]error, 0, len(ordered))
	for _, endpoint := range ordered {
		reader, retryErr := store.openEndpoint(ctx, endpoint, value)
		if retryErr == nil {
			return reader, nil
		}
		errorsByNode = append(errorsByNode, retryErr)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("open snapshot from all live replicas: %w", errors.Join(errorsByNode...))
}

func orderedSnapshotEndpoints(recorded []snapshot.Location, live []DaemonEndpoint, digest snapshot.Digest) []DaemonEndpoint {
	byNode := make(map[string]DaemonEndpoint, len(live))
	for _, endpoint := range live {
		byNode[endpoint.NodeName] = endpoint
	}
	recordedNodes := make([]string, 0, len(recorded))
	seenRecorded := make(map[string]struct{})
	for _, location := range recorded {
		if validateSnapshotLocation(location, digest) != nil {
			continue
		}
		if _, liveNow := byNode[location.Node]; !liveNow {
			continue
		}
		if _, duplicate := seenRecorded[location.Node]; duplicate {
			continue
		}
		seenRecorded[location.Node] = struct{}{}
		recordedNodes = append(recordedNodes, location.Node)
	}
	sort.Strings(recordedNodes)
	ordered := make([]DaemonEndpoint, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	for _, node := range recordedNodes {
		ordered = append(ordered, byNode[node])
		seen[node] = struct{}{}
	}
	for _, endpoint := range live {
		if _, found := seen[endpoint.NodeName]; found {
			continue
		}
		ordered = append(ordered, endpoint)
	}
	return ordered
}

func (store *SnapshotContentStore) openEndpoint(ctx context.Context, endpoint DaemonEndpoint, value snapshot.Snapshot) (io.ReadCloser, error) {
	key := snapshotKeyForDigest(value.Digest)
	target, err := store.daemon.snapshotURL(endpoint, key)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	client, err := store.daemon.snapshotHTTPClient()
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("open snapshot from %s: %w", endpoint.NodeName, err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("open snapshot from %s: daemon status %d", endpoint.NodeName, response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != value.ByteSize {
		response.Body.Close()
		return nil, fmt.Errorf("open snapshot from %s: declared length %d does not match manifest length %d", endpoint.NodeName, response.ContentLength, value.ByteSize)
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType != "application/x-tar" {
		response.Body.Close()
		return nil, fmt.Errorf("open snapshot from %s: unexpected content type %q", endpoint.NodeName, mediaType)
	}
	return &verifyingSnapshotReadCloser{
		body:           response.Body,
		hash:           sha256.New(),
		expectedDigest: value.Digest,
		expectedLength: value.ByteSize,
	}, nil
}

// verifyingSnapshotReadCloser validates only when EOF is reached so large
// archives remain streaming. Callers must discard partial output if the
// terminal Read reports a byte-count or digest mismatch.
type verifyingSnapshotReadCloser struct {
	body           io.ReadCloser
	hash           hash.Hash
	expectedDigest snapshot.Digest
	expectedLength int64
	read           int64
	verified       bool
}

func (reader *verifyingSnapshotReadCloser) Read(buffer []byte) (int, error) {
	n, err := reader.body.Read(buffer)
	if n > 0 {
		_, _ = reader.hash.Write(buffer[:n])
		reader.read += int64(n)
	}
	if errors.Is(err, io.EOF) && !reader.verified {
		reader.verified = true
		if reader.read != reader.expectedLength {
			return n, fmt.Errorf("snapshot byte count mismatch at EOF: got %d, want %d", reader.read, reader.expectedLength)
		}
		actual := snapshot.Digest("sha256:" + hex.EncodeToString(reader.hash.Sum(nil)))
		if actual != reader.expectedDigest {
			return n, fmt.Errorf("snapshot digest mismatch at EOF: got %s, want %s", actual, reader.expectedDigest)
		}
	}
	return n, err
}

func (reader *verifyingSnapshotReadCloser) Close() error { return reader.body.Close() }

func (store *SnapshotContentStore) Exists(ctx context.Context, location snapshot.Location) (bool, error) {
	endpoint, err := store.endpointForLocation(ctx, location)
	if err != nil {
		return false, err
	}
	status, err := store.locationRequest(ctx, http.MethodHead, endpoint, location)
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("HEAD snapshot on %s returned status %d", location.Node, status)
	}
}

func (store *SnapshotContentStore) DeleteLocation(ctx context.Context, location snapshot.Location) error {
	endpoint, err := store.endpointForLocation(ctx, location)
	if err != nil {
		return err
	}
	status, err := store.locationRequest(ctx, http.MethodDelete, endpoint, location)
	if err != nil {
		return err
	}
	if status != http.StatusNoContent && status != http.StatusNotFound {
		return fmt.Errorf("DELETE snapshot on %s returned status %d", location.Node, status)
	}
	return nil
}

func (store *SnapshotContentStore) DeleteAll(ctx context.Context, digest snapshot.Digest) error {
	if err := digest.Validate(); err != nil {
		return err
	}
	endpoints, err := store.daemon.DaemonEndpoints(ctx)
	if err != nil {
		return err
	}
	if len(endpoints) == 0 {
		return fmt.Errorf("no identified artifact daemon endpoints available")
	}
	location := snapshot.Location{Digest: digest, Driver: SnapshotDaemonDriver, Key: snapshotKeyForDigest(digest)}
	errorsByNode := make(chan error, len(endpoints))
	var wait sync.WaitGroup
	for _, endpoint := range endpoints {
		endpoint := endpoint
		wait.Add(1)
		go func() {
			defer wait.Done()
			locationForNode := location
			locationForNode.Node = endpoint.NodeName
			status, requestErr := store.locationRequest(ctx, http.MethodDelete, endpoint, locationForNode)
			if requestErr != nil {
				errorsByNode <- fmt.Errorf("delete snapshot from %s: %w", endpoint.NodeName, requestErr)
				return
			}
			if status != http.StatusNoContent && status != http.StatusNotFound {
				errorsByNode <- fmt.Errorf("delete snapshot from %s: daemon status %d", endpoint.NodeName, status)
			}
		}()
	}
	wait.Wait()
	close(errorsByNode)
	var collected []error
	for deleteErr := range errorsByNode {
		collected = append(collected, deleteErr)
	}
	return errors.Join(collected...)
}

func (store *SnapshotContentStore) endpointForLocation(ctx context.Context, location snapshot.Location) (DaemonEndpoint, error) {
	if err := validateSnapshotLocation(location, location.Digest); err != nil {
		return DaemonEndpoint{}, err
	}
	endpoints, err := store.daemon.DaemonEndpoints(ctx)
	if err != nil {
		return DaemonEndpoint{}, err
	}
	for _, endpoint := range endpoints {
		if endpoint.NodeName == location.Node {
			return endpoint, nil
		}
	}
	return DaemonEndpoint{}, fmt.Errorf("snapshot daemon node %q is not currently reachable", location.Node)
}

func validateSnapshotLocation(location snapshot.Location, digest snapshot.Digest) error {
	if err := location.Validate(); err != nil {
		return err
	}
	if location.Digest != digest || location.Driver != SnapshotDaemonDriver || location.Key != snapshotKeyForDigest(digest) {
		return fmt.Errorf("invalid jetbridge snapshot location")
	}
	if strings.TrimSpace(location.Node) == "" || location.Node != strings.TrimSpace(location.Node) {
		return fmt.Errorf("snapshot location requires a stable node name")
	}
	return nil
}

func (store *SnapshotContentStore) locationRequest(ctx context.Context, method string, endpoint DaemonEndpoint, location snapshot.Location) (int, error) {
	if err := validateSnapshotLocation(location, location.Digest); err != nil {
		return 0, err
	}
	target, err := store.daemon.snapshotURL(endpoint, location.Key)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return 0, err
	}
	if store.daemon.initializationErr != nil {
		return 0, store.daemon.initializationErr
	}
	client := store.daemon.client
	if client == nil {
		return 0, fmt.Errorf("daemon probe client is required")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode, nil
}
