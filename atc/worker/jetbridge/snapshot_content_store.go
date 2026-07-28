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
	tempDir           string
}

var _ snapshot.ContentStore = (*SnapshotContentStore)(nil)
var _ snapshot.ReplicaRepairer = (*SnapshotContentStore)(nil)

type SnapshotContentStoreOption func(*SnapshotContentStore) error

// WithSnapshotContentTempDir routes complete upload and repair spools through
// the same dedicated disk-backed scratch directory as canonicalization.
func WithSnapshotContentTempDir(tempDir string) SnapshotContentStoreOption {
	return func(store *SnapshotContentStore) error {
		if tempDir == "" {
			return fmt.Errorf("snapshot content temp directory is required")
		}
		if err := snapshot.ValidateTempDir(tempDir); err != nil {
			return err
		}
		store.tempDir = tempDir
		return nil
	}
}

func NewSnapshotContentStore(
	daemon *DaemonClient,
	locations SnapshotLocationResolver,
	replicationFactor int,
	archiveLimits snapshot.ArchiveLimits,
	options ...SnapshotContentStoreOption,
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
	store := &SnapshotContentStore{
		daemon:            daemon,
		locations:         locations,
		replicationFactor: replicationFactor,
		maxBytes:          maxBytes,
		archiveLimits:     archiveLimits,
	}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("snapshot content store option is required")
		}
		if err := option(store); err != nil {
			return nil, fmt.Errorf("snapshot content store option: %w", err)
		}
	}
	return store, nil
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
	spool, size, actualDigest, err := spoolSnapshot(ctx, source, store.maxBytes, store.tempDir)
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

func spoolSnapshot(ctx context.Context, source io.Reader, maxBytes int64, tempDir string) (*os.File, int64, snapshot.Digest, error) {
	spool, err := os.CreateTemp(tempDir, ".jetbridge-snapshot-*")
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
	key := snapshotKeyForDigest(digest)
	location := snapshot.Location{Digest: digest, Driver: SnapshotDaemonDriver, Key: key, Node: endpoint.NodeName}
	status, err := store.putEndpointRequest(ctx, endpoint, key, spoolName, size)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: %w", endpoint.NodeName, err)
	}
	if status == http.StatusCreated || status == http.StatusOK {
		return location, nil
	}
	if status != http.StatusConflict {
		return snapshot.Location{}, fmt.Errorf("upload snapshot to %s: daemon status %d", endpoint.NodeName, status)
	}

	// The namespace is immutable, so a conflict is normally an already-present
	// object. Reuse it only after a complete digest verification. If the key is
	// present but corrupt, the caller-provided spool is itself digest-verified,
	// which makes delete-and-rewrite safe even when this is the only replica.
	verified, err := store.Exists(ctx, location)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("verify conflicting snapshot on %s: %w", endpoint.NodeName, err)
	}
	if verified {
		return location, nil
	}
	if err := store.DeleteLocation(ctx, location); err != nil {
		return snapshot.Location{}, fmt.Errorf("replace corrupt snapshot on %s: %w", endpoint.NodeName, err)
	}
	status, err = store.putEndpointRequest(ctx, endpoint, key, spoolName, size)
	if err != nil {
		return snapshot.Location{}, fmt.Errorf("rewrite snapshot on %s: %w", endpoint.NodeName, err)
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return snapshot.Location{}, fmt.Errorf("rewrite snapshot on %s: daemon status %d", endpoint.NodeName, status)
	}
	return location, nil
}

func (store *SnapshotContentStore) putEndpointRequest(
	ctx context.Context,
	endpoint DaemonEndpoint,
	key string,
	spoolName string,
	size int64,
) (status int, err error) {
	file, err := os.Open(spoolName)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	target, err := store.daemon.snapshotURL(endpoint, key)
	if err != nil {
		return 0, err
	}
	// NopCloser keeps ownership of the spool file here. *os.File is an
	// io.ReadCloser, so handing it over directly makes it the request body, and
	// the transport closes a request body it is given — "even on errors", per
	// net/http. The deferred Close above then races it, and when the transport
	// wins, Close returns os.ErrClosed, which errors.Join folds into the named
	// return of an upload that in fact succeeded.
	//
	// That is the whole of the intermittent snapshot failure: a won race left
	// zero replicas acknowledged, so the snapshot reported content_unavailable
	// and every consumer saw a 503. Losing the race looked perfectly healthy,
	// which is why it presented as flakiness rather than a defect.
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target.String(), io.NopCloser(file))
	if err != nil {
		return 0, err
	}
	request.ContentLength = size
	request.Header.Set("Content-Type", "application/x-tar")
	client, err := store.daemon.snapshotHTTPClient()
	if err != nil {
		return 0, err
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_, drainErr := io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	closeErr := response.Body.Close()
	if drainErr != nil || closeErr != nil {
		return 0, errors.Join(drainErr, closeErr)
	}
	return response.StatusCode, nil
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

func (store *SnapshotContentStore) RepairReplicas(
	ctx context.Context,
	value snapshot.Snapshot,
	recorded []snapshot.Location,
) (snapshot.ReplicaRepairResult, error) {
	result := snapshot.ReplicaRepairResult{}
	if err := value.Validate(); err != nil {
		return result, err
	}
	if value.ContentState != snapshot.ContentStateAvailable {
		return result, snapshot.ErrExpired
	}

	recordedByNode := make(map[string]snapshot.Location, len(recorded))
	for _, location := range recorded {
		if err := validateSnapshotLocation(location, value.Digest); err != nil {
			return result, err
		}
		if prior, found := recordedByNode[location.Node]; found && prior != location {
			return result, fmt.Errorf("conflicting snapshot locations for node %q", location.Node)
		}
		recordedByNode[location.Node] = location
	}

	endpoints, err := store.daemon.DaemonEndpoints(ctx)
	if err != nil {
		return result, err
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].NodeName < endpoints[j].NodeName })
	result.LiveCapacity = len(endpoints)
	result.Desired = store.replicationFactor
	if result.Desired > result.LiveCapacity {
		result.Desired = result.LiveCapacity
	}
	if result.LiveCapacity == 0 {
		return result, fmt.Errorf("%w: no live artifact daemon endpoints", snapshot.ErrNoReadableReplica)
	}
	liveByNode := make(map[string]DaemonEndpoint, len(endpoints))
	for _, endpoint := range endpoints {
		liveByNode[endpoint.NodeName] = endpoint
	}

	verified := make(map[string]bool, len(endpoints))
	suspect := make(map[string]error, len(recorded))
	unknown := make(map[string]error, len(recorded))
	var sourcePath string
	defer func() {
		if sourcePath != "" {
			_ = os.Remove(sourcePath)
		}
	}()

	recordedNodes := make([]string, 0, len(recordedByNode))
	for node := range recordedByNode {
		recordedNodes = append(recordedNodes, node)
	}
	sort.Strings(recordedNodes)
	for _, node := range recordedNodes {
		if result.Verified >= result.Desired {
			break
		}
		endpoint, live := liveByNode[node]
		if !live {
			// Absence from discovery is not proof that the recorded bytes are
			// gone. Keep the row until the stable node is addressable and an
			// exact-node request proves it missing (or corrupt bytes are deleted).
			unknown[node] = fmt.Errorf("recorded node is not currently discoverable")
			continue
		}
		path, verifyErr := store.verifyRepairEndpoint(ctx, endpoint, value, sourcePath == "")
		if verifyErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return sortedReplicaRepairResult(result), ctxErr
			}
			if errors.Is(verifyErr, snapshot.ErrReplicaMissing) || errors.Is(verifyErr, snapshot.ErrReplicaCorrupt) {
				suspect[node] = verifyErr
			} else {
				unknown[node] = verifyErr
			}
			continue
		}
		verified[node] = true
		result.Verified++
		if sourcePath == "" {
			sourcePath = path
		}
	}

	// Probe unrecorded live nodes only when no recorded location supplied a
	// verified source. Stop at the first recovered acknowledgement: subsequent
	// replicas are restored with deterministic PUTs from this verified spool,
	// which avoids adopting arbitrary over-factor copies.
	if sourcePath == "" {
		for _, endpoint := range endpoints {
			if _, recorded := recordedByNode[endpoint.NodeName]; recorded {
				continue
			}
			path, verifyErr := store.verifyRepairEndpoint(ctx, endpoint, value, true)
			if verifyErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return sortedReplicaRepairResult(result), ctxErr
				}
				if errors.Is(verifyErr, snapshot.ErrReplicaMissing) || errors.Is(verifyErr, snapshot.ErrReplicaCorrupt) {
					suspect[endpoint.NodeName] = verifyErr
				} else {
					unknown[endpoint.NodeName] = verifyErr
				}
				continue
			}
			verified[endpoint.NodeName] = true
			result.Verified++
			result.Added = appendUniqueSnapshotLocation(result.Added, snapshot.Location{
				Digest: value.Digest, Driver: SnapshotDaemonDriver,
				Key: snapshotKeyForDigest(value.Digest), Node: endpoint.NodeName,
			})
			sourcePath = path
			break
		}
	}

	if sourcePath == "" {
		return sortedReplicaRepairResult(result), errors.Join(snapshot.ErrNoReadableReplica, joinedNodeErrors(unknown))
	}

	cleaned := make(map[string]bool)
	var copyErrors []error
	var cleanupErrors []error
	for _, endpoint := range endpoints {
		if result.Verified >= result.Desired {
			break
		}
		if verified[endpoint.NodeName] || unknown[endpoint.NodeName] != nil {
			continue
		}
		location := snapshot.Location{
			Digest: value.Digest, Driver: SnapshotDaemonDriver,
			Key: snapshotKeyForDigest(value.Digest), Node: endpoint.NodeName,
		}
		if errors.Is(suspect[endpoint.NodeName], snapshot.ErrReplicaCorrupt) {
			if err := store.DeleteLocation(ctx, location); err != nil {
				copyErrors = append(copyErrors, err)
				continue
			}
			cleaned[endpoint.NodeName] = true
		}
		added, putErr := store.putEndpoint(ctx, endpoint, value.Digest, sourcePath, value.ByteSize)
		if putErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return sortedReplicaRepairResult(result), ctxErr
			}
			copyErrors = append(copyErrors, putErr)
			continue
		}
		verified[endpoint.NodeName] = true
		result.Verified++
		result.Added = appendUniqueSnapshotLocation(result.Added, added)
	}

	// Prune only definitive stale rows, and only after a source was verified.
	// Unknown transport/TLS/5xx outcomes retain their rows for a later pass.
	for _, node := range recordedNodes {
		if verified[node] || unknown[node] != nil {
			continue
		}
		if !errors.Is(suspect[node], snapshot.ErrReplicaMissing) &&
			!errors.Is(suspect[node], snapshot.ErrReplicaCorrupt) {
			// The factor may have been satisfied before this recorded row was
			// examined. Unobserved is not stale.
			continue
		}
		location := recordedByNode[node]
		if errors.Is(suspect[node], snapshot.ErrReplicaCorrupt) && !cleaned[node] {
			endpoint := liveByNode[node]
			status, deleteErr := store.locationRequest(ctx, http.MethodDelete, endpoint, location)
			if deleteErr != nil || (status != http.StatusNoContent && status != http.StatusNotFound) {
				if deleteErr == nil {
					deleteErr = fmt.Errorf("DELETE snapshot on %s returned status %d", node, status)
				}
				cleanupErrors = append(cleanupErrors, deleteErr)
				continue
			}
		}
		result.Removed = appendUniqueSnapshotLocation(result.Removed, location)
	}

	if result.Verified < result.Desired {
		copyErrors = append(copyErrors, fmt.Errorf(
			"snapshot replica repair verified %d replicas, want %d", result.Verified, result.Desired,
		))
		if err := joinedNodeErrors(unknown); err != nil {
			copyErrors = append(copyErrors, err)
		}
	} else {
		// Once the configured factor is verified, failures from optional target
		// attempts or unrelated recorded nodes must not turn a healthy digest
		// into a perpetual repair error.
		copyErrors = nil
	}
	return sortedReplicaRepairResult(result), errors.Join(errors.Join(copyErrors...), errors.Join(cleanupErrors...))
}

// verifyRepairEndpoint reaches verified EOF. When spool is true it returns a
// private, synced source path owned by the caller; otherwise it returns no
// path and never retains the complete archive.
func (store *SnapshotContentStore) verifyRepairEndpoint(
	ctx context.Context,
	endpoint DaemonEndpoint,
	value snapshot.Snapshot,
	spool bool,
) (string, error) {
	reader, err := store.openEndpoint(ctx, endpoint, value)
	if err != nil {
		return "", err
	}
	if !spool {
		_, readErr := io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		return "", errors.Join(readErr, closeErr)
	}
	file, size, digest, spoolErr := spoolSnapshot(ctx, reader, store.maxBytes, store.tempDir)
	closeReaderErr := reader.Close()
	if spoolErr != nil {
		return "", errors.Join(spoolErr, closeReaderErr)
	}
	if file == nil {
		return "", errors.Join(fmt.Errorf("snapshot replica spool returned no file"), closeReaderErr)
	}
	name := file.Name()
	if closeReaderErr != nil {
		closeFileErr := file.Close()
		removeErr := os.Remove(name)
		return "", errors.Join(closeReaderErr, closeFileErr, removeErr)
	}
	if size != value.ByteSize || digest != value.Digest {
		_ = file.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("%w: replica identity does not match manifest", snapshot.ErrReplicaCorrupt)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func appendUniqueSnapshotLocation(locations []snapshot.Location, location snapshot.Location) []snapshot.Location {
	for _, existing := range locations {
		if existing == location {
			return locations
		}
	}
	return append(locations, location)
}

func sortedReplicaRepairResult(result snapshot.ReplicaRepairResult) snapshot.ReplicaRepairResult {
	less := func(locations []snapshot.Location, left, right int) bool {
		if locations[left].Driver != locations[right].Driver {
			return locations[left].Driver < locations[right].Driver
		}
		if locations[left].Key != locations[right].Key {
			return locations[left].Key < locations[right].Key
		}
		return locations[left].Node < locations[right].Node
	}
	sort.Slice(result.Added, func(left, right int) bool { return less(result.Added, left, right) })
	sort.Slice(result.Removed, func(left, right int) bool { return less(result.Removed, left, right) })
	return result
}

func joinedNodeErrors(errorsByNode map[string]error) error {
	nodes := make([]string, 0, len(errorsByNode))
	for node := range errorsByNode {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	joined := make([]error, 0, len(nodes))
	for _, node := range nodes {
		joined = append(joined, fmt.Errorf("snapshot replica %s: %w", node, errorsByNode[node]))
	}
	return errors.Join(joined...)
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
		if response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: snapshot on %s", snapshot.ErrReplicaMissing, endpoint.NodeName)
		}
		return nil, fmt.Errorf("open snapshot from %s: daemon status %d", endpoint.NodeName, response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != value.ByteSize {
		response.Body.Close()
		return nil, fmt.Errorf("%w: snapshot on %s declared length %d does not match manifest length %d", snapshot.ErrReplicaCorrupt, endpoint.NodeName, response.ContentLength, value.ByteSize)
	}
	mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if mediaType != "application/x-tar" {
		response.Body.Close()
		return nil, fmt.Errorf("%w: snapshot on %s has unexpected content type %q", snapshot.ErrReplicaCorrupt, endpoint.NodeName, mediaType)
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
			return n, fmt.Errorf("%w: snapshot byte count mismatch at EOF: got %d, want %d", snapshot.ErrReplicaCorrupt, reader.read, reader.expectedLength)
		}
		actual := snapshot.Digest("sha256:" + hex.EncodeToString(reader.hash.Sum(nil)))
		if actual != reader.expectedDigest {
			return n, fmt.Errorf("%w: snapshot digest mismatch at EOF: got %s, want %s", snapshot.ErrReplicaCorrupt, actual, reader.expectedDigest)
		}
	}
	return n, err
}

func (reader *verifyingSnapshotReadCloser) Close() error { return reader.body.Close() }

func (store *SnapshotContentStore) Exists(ctx context.Context, location snapshot.Location) (exists bool, err error) {
	endpoint, err := store.endpointForLocation(ctx, location)
	if err != nil {
		return false, err
	}
	target, err := store.daemon.snapshotURL(endpoint, location.Key)
	if err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false, err
	}
	client, err := store.daemon.snapshotHTTPClient()
	if err != nil {
		return false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	switch response.StatusCode {
	case http.StatusNotFound:
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, nil
	case http.StatusOK:
		mediaType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
		if mediaType != "application/x-tar" {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			return false, nil
		}
		hasher := sha256.New()
		read, copyErr := io.Copy(hasher, io.LimitReader(response.Body, store.maxBytes+1))
		if copyErr != nil {
			return false, copyErr
		}
		if read > store.maxBytes {
			return false, nil
		}
		actual := snapshot.Digest("sha256:" + hex.EncodeToString(hasher.Sum(nil)))
		return actual == location.Digest, nil
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, fmt.Errorf("GET snapshot on %s returned status %d", location.Node, response.StatusCode)
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
