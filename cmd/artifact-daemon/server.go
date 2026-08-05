package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/artifactcap"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/snapshot"
	"golang.org/x/sys/unix"
)

// Server is the artifact-daemon HTTP server that stores and serves
// artifact tar files from local hostPath storage.
type Server struct {
	logger           lager.Logger
	storagePath      string
	nodeName         string
	registry         *Registry
	peers            *PeerResolver
	mirrorTrigger    func(ctx context.Context, key string)
	metrics          *metrics
	guard            *ReadGuard
	snapshotMaxBytes int64
	resolveSlots     chan struct{}
	resolveTimeout   time.Duration
	hangar           hangar.Store
	snapshotCacheMu  sync.Mutex

	// snapshotRepairSlots bounds concurrent durable-metadata repairs. Each one
	// downloads and decompresses a whole object to prove it, so admission is
	// capped and excess requests are refused rather than queued.
	snapshotRepairSlots chan struct{}

	// Injected only by package-internal durability tests. Production always
	// uses syncRootDirectory so a successful convergence response means the
	// namespace mutation (or observed steady state) was re-synchronized.
	syncSnapshotDirectory func(*os.Root, string) error
	copyHooks             anchoredCopyHooks
	serveHooks            anchoredServeHooks
	mutationHooks         anchoredMutationHooks
}

type anchoredCopyHooks struct {
	sourceOpened               func()
	destinationParentOpened    func()
	temporaryReady             func(string)
	destinationEntryPublishing func()
}

type anchoredServeHooks struct {
	sourceDescriptorOpened func()
	sourceOpened           func()
}

type anchoredMutationHooks struct {
	destinationParentOpened    func()
	temporaryOpened            func(*os.File, string)
	streamInDestinationRemoved func()
}

func (s *Server) openGenericArtifact(candidate string) (*os.File, os.FileInfo, error) {
	rel, err := pathBelow(s.storagePath, candidate)
	if err != nil {
		return nil, nil, err
	}
	if snapshotNamespaceKey(filepath.ToSlash(rel)) {
		return nil, nil, fmt.Errorf("snapshot namespace is reserved")
	}
	if daemonPrivateNamespaceKey(filepath.ToSlash(rel)) {
		return nil, nil, fmt.Errorf("daemon-private namespace is reserved")
	}
	root, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	return openPathAtNoFollow(root, rel)
}

func (s *Server) openGenericArtifactWithReadGuard(ctx context.Context, candidate string) (*os.File, os.FileInfo, func(), error) {
	release, err := s.guard.BeginReadContext(ctx, s.stepHandle(candidate))
	if err != nil {
		return nil, nil, nil, err
	}
	file, info, err := s.openGenericArtifact(candidate)
	if err != nil {
		release()
		return nil, nil, nil, err
	}
	return file, info, release, nil
}

func (s *Server) openRegisteredArtifactWithReadGuard(ctx context.Context, key string) (*os.File, os.FileInfo, string, func(), error) {
	for range 8 {
		candidate, found := s.registry.Lookup(key)
		if !found {
			return nil, nil, "", nil, os.ErrNotExist
		}
		// Lexical containment is stable and can reject outside authority before
		// locking. Existence and no-symlink validation must happen only after
		// acquiring the handle guard: stream-in deliberately has a short
		// remove-old/rename-new window while holding that handle exclusively.
		if _, err := pathBelow(filepath.Join(s.storagePath, "steps"), candidate); err != nil {
			s.registry.RemoveIf(key, candidate)
			return nil, nil, "", nil, os.ErrNotExist
		}
		release, err := s.guard.BeginReadContext(ctx, s.stepHandle(candidate))
		if err != nil {
			return nil, nil, "", nil, err
		}
		current, stillRegistered := s.registry.Lookup(key)
		if !stillRegistered || current != candidate {
			release()
			continue
		}
		if _, err := s.validateStepPath(candidate, true); err != nil {
			s.registry.RemoveIf(key, candidate)
			release()
			return nil, nil, "", nil, os.ErrNotExist
		}
		file, info, err := s.openGenericArtifact(candidate)
		if err != nil {
			release()
			if errors.Is(err, os.ErrNotExist) {
				s.registry.RemoveIf(key, candidate)
			}
			return nil, nil, "", nil, err
		}
		return file, info, candidate, release, nil
	}
	return nil, nil, "", nil, fmt.Errorf("registry alias changed repeatedly during guarded open")
}

func (s *Server) openRegistryAliasForRequestWithReadGuard(ctx context.Context, r *http.Request) (*os.File, os.FileInfo, string, func(), error) {
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	keys := []string{key}
	if stripped := strings.TrimPrefix(key, "steps/"); stripped != key {
		keys = append(keys, stripped)
	}
	for _, candidateKey := range keys {
		file, info, candidate, release, err := s.openRegisteredArtifactWithReadGuard(ctx, candidateKey)
		if err == nil {
			return file, info, candidate, release, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, nil, "", nil, err
		}
	}
	return nil, nil, "", nil, os.ErrNotExist
}

const snapshotKeyPrefix = "snapshots/sha256/"

// snapshotCacheOnlyDeleteHeader lets the ATC clean a corrupted legacy daemon
// cache entry without deleting the authoritative Hangar object. Only the
// canonical hangar-v1 location may request a durable deletion.
const snapshotCacheOnlyDeleteHeader = "X-Concourse-Snapshot-Delete-Cache-Only"

var defaultSnapshotMaxBytes = func() int64 {
	limit, err := snapshot.CanonicalArchiveByteLimit(snapshot.DefaultMaxSnapshotContentBytes, snapshot.DefaultMaxSnapshotEntries)
	if err != nil {
		panic(err)
	}
	return limit
}()

const (
	defaultResolveMaxConcurrent = 32
	defaultResolveTimeout       = 30 * time.Minute

	// Durable-metadata repair is background recovery, not request serving. One
	// at a time is enough to drain a bucket over successive repair passes and
	// keeps the proving cost off the path of live snapshot reads.
	defaultSnapshotRepairMaxConcurrent = 1
)

// NewServer creates a new artifact-daemon server.
func NewServer(logger lager.Logger, storagePath, nodeName string) *Server {
	return &Server{
		logger:                logger,
		storagePath:           storagePath,
		nodeName:              nodeName,
		registry:              NewRegistry(logger),
		metrics:               newMetrics(),
		guard:                 NewReadGuard(),
		snapshotMaxBytes:      defaultSnapshotMaxBytes,
		resolveSlots:          make(chan struct{}, defaultResolveMaxConcurrent),
		resolveTimeout:        defaultResolveTimeout,
		snapshotRepairSlots:   make(chan struct{}, defaultSnapshotRepairMaxConcurrent),
		syncSnapshotDirectory: syncRootDirectory,
	}
}

// ConfigureResolveLimits sets daemon-wide resolve admission and the maximum
// lifetime of one local or peer resolution. It must be called before Handler
// begins serving requests.
func (s *Server) ConfigureResolveLimits(maxConcurrent int, timeout time.Duration) error {
	if maxConcurrent <= 0 {
		return fmt.Errorf("resolve max concurrency must be positive")
	}
	if timeout <= 0 {
		return fmt.Errorf("resolve timeout must be positive")
	}
	s.resolveSlots = make(chan struct{}, maxConcurrent)
	s.resolveTimeout = timeout
	return nil
}

// Guard returns the read/sweep coordination guard. The sweeper takes its
// exclusive side per directory removal so reads never copy from a directory
// being deleted.
func (s *Server) Guard() *ReadGuard {
	return s.guard
}

// stepHandle returns the steps/{handle} segment guarding a path under the
// steps root, or the path itself for non-steps paths (legacy flat files) so
// they still get a consistent per-path lock.
func (s *Server) stepHandle(path string) string {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	rel, err := filepath.Rel(stepsRoot, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return strings.Split(rel, string(filepath.Separator))[0]
}

// MirrorOriginHeader marks a PUT /stream-in as originating from a peer
// daemon's mirror. Such writes must not re-trigger mirroring: the origin
// daemon fans out to all chosen peers itself, and a re-trigger makes peers
// ping-pong the same key forever, each racy hop able to propagate a
// truncated copy.
const MirrorOriginHeader = "X-Concourse-Mirror"

// Registry returns the server's artifact registry.
func (s *Server) Registry() *Registry {
	return s.registry
}

// SetPeerResolver configures the peer resolver for cross-node artifact
// resolution. When nil, /resolve only checks local storage.
func (s *Server) SetPeerResolver(peers *PeerResolver) {
	s.peers = peers
}

// SetMirrorTrigger wires the outbound mirror manager so that handleStreamIn
// and POST /mirror schedule a background replication after the local data
// settles. Pass nil (or skip the call) to disable mirroring.
func (s *Server) SetMirrorTrigger(trigger func(ctx context.Context, key string)) {
	s.mirrorTrigger = trigger
}

// SetHangarStore makes Hangar the durable authority for immutable snapshots.
// The hostPath remains an on-use cache; callers only receive a successful PUT
// after the canonical bytes have been committed to Hangar.
func (s *Server) SetHangarStore(store hangar.Store) {
	s.hangar = store
}

// Handler returns the HTTP handler for the server. When tlsEnabled is true,
// protected routes are wrapped with requireClientCert middleware that returns
// 401 if the request lacks a verified client certificate. Resolve routes do
// not require a client certificate because build init containers cannot hold
// one; production config independently requires an exact short-lived resolve
// capability on every item.
func (s *Server) Handler(opts ...HandlerOption) http.Handler {
	cfg := handlerConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	mux := http.NewServeMux()

	// protect wraps a handler with mTLS enforcement when TLS is enabled.
	protect := func(h http.HandlerFunc) http.HandlerFunc {
		if cfg.tlsEnabled {
			return requireClientCert(h)
		}
		return h
	}

	// Routes without client-certificate enforcement. Kubelet probes and
	// Prometheus scrapers cannot present client certs; resolve routes apply
	// their independent capability verifier before any lookup or mutation.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /resolve", func(w http.ResponseWriter, r *http.Request) {
		s.handleResolve(w, r, cfg)
	})
	mux.HandleFunc("POST /resolve-batch", func(w http.ResponseWriter, r *http.Request) {
		s.handleResolveBatch(w, r, cfg)
	})
	if s.metrics != nil {
		mux.Handle("GET /metrics", s.metrics.handler())
	}

	// Protected paths — require client cert when TLS is enabled.
	mux.HandleFunc("GET /artifacts/", protect(s.handleGetArtifact))
	mux.HandleFunc("PUT /artifacts/", protect(s.handlePutArtifact))
	mux.HandleFunc("DELETE /artifacts/", protect(s.handleDeleteArtifact))
	mux.HandleFunc("HEAD /artifacts/", protect(s.handleHeadArtifact))
	mux.HandleFunc("POST /register", protect(s.handleRegister))
	mux.HandleFunc("POST /mirror", protect(s.handleMirrorTrigger))
	mux.HandleFunc("PUT /stream-in/", protect(s.handleStreamIn))
	mux.HandleFunc("HEAD /resource-caches/", protect(s.handleHeadResourceCache))
	mux.HandleFunc("GET /resource-caches/", protect(s.handleGetResourceCache))
	mux.HandleFunc(
		"POST /snapshots/v1/repair-durable-metadata/{digest}",
		protect(s.handleRepairSnapshotDurableMetadata),
	)
	mux.HandleFunc("GET /snapshots/v1/durable-objects", protect(s.handleDurableSnapshotInventory))
	mux.HandleFunc("DELETE /snapshots/v1/durable-objects/{digest}", protect(s.handleDeleteDurableSnapshotObject))

	// net/http's ServeMux canonicalizes traversal-looking paths before route
	// selection. Validate artifact paths first so malformed paths receive the
	// contractually required 400 instead of a redirect.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/artifacts/") || strings.HasPrefix(r.URL.EscapedPath(), "/artifacts/") {
			if _, err := s.artifactPath(r); err != nil {
				http.Error(w, "malformed artifact path", http.StatusBadRequest)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/stream-in/") || strings.HasPrefix(r.URL.EscapedPath(), "/stream-in/") {
			key, err := canonicalRequestKey(r.URL.Path, r.URL.EscapedPath(), "/stream-in/")
			if err != nil || snapshotNamespaceKey(key) {
				http.Error(w, "malformed stream-in key", http.StatusBadRequest)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

// HandlerOption configures the HTTP handler.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	tlsEnabled                bool
	resolveCapabilityRequired bool
	resolveCapabilityVerifier *artifactcap.Verifier
}

// WithTLS enables mTLS enforcement on protected routes.
func WithTLS() HandlerOption {
	return func(c *handlerConfig) {
		c.tlsEnabled = true
	}
}

// WithResolveCapabilityKey requires every resolve operation to carry a valid
// short-lived capability bound to its exact source key and destination. An
// invalid key deliberately leaves the handler fail-closed.
func WithResolveCapabilityKey(key []byte) HandlerOption {
	return func(c *handlerConfig) {
		c.resolveCapabilityRequired = true
		c.resolveCapabilityVerifier, _ = artifactcap.NewVerifier(key)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) artifactPath(r *http.Request) (string, error) {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/artifacts/")
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	if key == r.URL.Path || key == "" || escaped == r.URL.EscapedPath() {
		return "", fmt.Errorf("invalid artifact key")
	}
	canonicalEscaped, err := canonicalEscapedKey(key)
	if err != nil || escaped != canonicalEscaped {
		return "", fmt.Errorf("non-canonical artifact key")
	}
	if daemonPrivateNamespaceKey(key) {
		return "", fmt.Errorf("daemon-private artifact key")
	}
	converted := filepath.FromSlash(key)
	if filepath.IsAbs(converted) {
		return "", fmt.Errorf("absolute artifact key")
	}
	joined := filepath.Join(s.storagePath, converted)
	rel, err := filepath.Rel(s.storagePath, joined)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path escapes storage root")
	}
	return joined, nil
}

func snapshotDigestForRequest(r *http.Request) (string, bool, error) {
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	if !snapshotNamespaceKey(key) {
		return "", false, nil
	}
	digest, err := snapshotDigestFromKey(key)
	return digest, true, err
}

func snapshotDigestFromKey(key string) (string, error) {
	if !strings.HasPrefix(key, snapshotKeyPrefix) {
		return "", fmt.Errorf("invalid snapshot namespace path")
	}
	name := strings.TrimPrefix(key, snapshotKeyPrefix)
	if len(name) != 68 || !strings.HasSuffix(name, ".tar") {
		return "", fmt.Errorf("invalid snapshot key")
	}
	digest := strings.TrimSuffix(name, ".tar")
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
		return "", fmt.Errorf("invalid snapshot digest")
	}
	return digest, nil
}

func snapshotKey(digest string) string {
	return snapshotKeyPrefix + digest + ".tar"
}

func (s *Server) handlePutSnapshot(w http.ResponseWriter, r *http.Request, expectedDigest string) {
	start := time.Now()
	status := "error"
	var copied int64
	defer func() { s.metrics.recordSnapshot("put", status, copied, time.Since(start)) }()
	if r.ContentLength > s.snapshotMaxBytes {
		http.Error(w, "snapshot upload exceeds maximum size", http.StatusRequestEntityTooLarge)
		return
	}

	if err := os.MkdirAll(s.storagePath, 0755); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	root, err := os.OpenRoot(s.storagePath)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer root.Close()

	key := snapshotKey(expectedDigest)
	parent := path.Dir(key)
	if err := s.ensureSnapshotNamespace(root); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	release := s.guard.BeginSweep(key)
	defer release()

	tmpKey, file, err := createSnapshotTemp(root, parent)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpExists := true
	defer func() {
		_ = file.Close()
		if tmpExists {
			_ = root.Remove(tmpKey)
		}
	}()

	hash := sha256.New()
	copied, err = copySnapshotContext(r.Context(), io.MultiWriter(file, hash), r.Body, s.snapshotMaxBytes)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, errSnapshotTooLarge) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "snapshot upload failed", code)
		return
	}
	actualDigest := hex.EncodeToString(hash.Sum(nil))
	if actualDigest != expectedDigest {
		status = "digest_mismatch"
		http.Error(w, "snapshot digest mismatch", http.StatusUnprocessableEntity)
		return
	}
	if err := file.Chmod(0644); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := file.Sync(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := file.Close(); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	exists, identical, err := compareSnapshot(r.Context(), root, key, tmpKey)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if exists {
		if identical {
			if err := s.syncSnapshotDirectory(root, parent); err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if err := s.ensureSnapshotInHangar(r.Context(), root, expectedDigest); err != nil {
				http.Error(w, "durable snapshot upload failed", http.StatusBadGateway)
				return
			}
			status = "identical"
			s.setHangarSnapshotLocationHeader(w)
			w.WriteHeader(http.StatusOK)
		} else {
			status = "conflict"
			http.Error(w, "snapshot content conflict", http.StatusConflict)
		}
		return
	}
	if err := root.Link(tmpKey, key); err != nil {
		if errors.Is(err, os.ErrExist) {
			exists, identical, compareErr := compareSnapshot(r.Context(), root, key, tmpKey)
			if compareErr != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if exists && identical {
				if err := s.syncSnapshotDirectory(root, parent); err != nil {
					http.Error(w, "internal error", http.StatusInternalServerError)
					return
				}
				if err := s.ensureSnapshotInHangar(r.Context(), root, expectedDigest); err != nil {
					http.Error(w, "durable snapshot upload failed", http.StatusBadGateway)
					return
				}
				status = "identical"
				s.setHangarSnapshotLocationHeader(w)
				w.WriteHeader(http.StatusOK)
				return
			}
			status = "conflict"
			http.Error(w, "snapshot content conflict", http.StatusConflict)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := root.Remove(tmpKey); err != nil {
		// The immutable final link is already durable content. Report failure so
		// callers retry; a retry compares identical bytes and converges.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpExists = false
	if err := s.syncSnapshotDirectory(root, parent); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.ensureSnapshotInHangar(r.Context(), root, expectedDigest); err != nil {
		http.Error(w, "durable snapshot upload failed", http.StatusBadGateway)
		return
	}
	status = "created"
	s.setHangarSnapshotLocationHeader(w)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) setHangarSnapshotLocationHeader(w http.ResponseWriter) {
	if s.hangar != nil {
		w.Header().Set("X-Concourse-Snapshot-Durable-Location", "hangar-v1")
	}
}

func (s *Server) ensureSnapshotNamespace(root *os.Root) error {
	for _, directory := range []struct {
		name   string
		parent string
	}{
		{name: "snapshots", parent: "."},
		{name: "snapshots/sha256", parent: "snapshots"},
	} {
		if err := root.Mkdir(directory.name, 0755); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(directory.name)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot namespace component %q is not a real directory", directory.name)
		}
		if err := s.syncSnapshotDirectory(root, directory.parent); err != nil {
			return err
		}
	}
	return s.syncSnapshotDirectory(root, "snapshots/sha256")
}

var errSnapshotTooLarge = errors.New("snapshot exceeds maximum size")

func copySnapshotContext(ctx context.Context, dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	buffer := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if total+int64(n) > maxBytes {
				return total, errSnapshotTooLarge
			}
			written, writeErr := dst.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func createSnapshotTemp(root *os.Root, parent string) (string, *os.File, error) {
	for range 100 {
		random := make([]byte, 16)
		if _, err := rand.Read(random); err != nil {
			return "", nil, err
		}
		name := path.Join(parent, ".snapshot-put-"+hex.EncodeToString(random))
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return name, file, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, fmt.Errorf("unable to allocate snapshot temporary file")
}

func compareSnapshot(ctx context.Context, root *os.Root, key, tmpKey string) (bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return false, false, err
	}
	info, err := root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.Mode().IsRegular() {
		return true, false, nil
	}
	tmpInfo, err := root.Stat(tmpKey)
	if err != nil {
		return false, false, err
	}
	if info.Size() != tmpInfo.Size() {
		return true, false, nil
	}
	existing, err := root.Open(key)
	if err != nil {
		return false, false, err
	}
	defer existing.Close()
	temporary, err := root.Open(tmpKey)
	if err != nil {
		return false, false, err
	}
	defer temporary.Close()
	left := make([]byte, 128*1024)
	right := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return false, false, err
		}
		leftN, leftErr := io.ReadFull(existing, left)
		rightN, rightErr := io.ReadFull(temporary, right)
		if leftN != rightN || !bytes.Equal(left[:leftN], right[:rightN]) {
			return true, false, nil
		}
		if errors.Is(leftErr, io.EOF) || errors.Is(leftErr, io.ErrUnexpectedEOF) {
			return true, errors.Is(rightErr, io.EOF) || errors.Is(rightErr, io.ErrUnexpectedEOF), nil
		}
		if leftErr != nil || rightErr != nil {
			return false, false, errors.Join(leftErr, rightErr)
		}
	}
}

func syncRootDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *Server) ensureSnapshotInHangar(ctx context.Context, root *os.Root, digest string) error {
	if s.hangar == nil {
		return nil
	}
	_, err := s.hangar.Inspect(ctx, hangar.KindSnapshot, hangar.Digest("sha256:"+digest), s.snapshotMaxBytes)
	if err == nil {
		return nil
	}
	if !errors.Is(err, hangar.ErrNotFound) {
		return fmt.Errorf("inspect snapshot in hangar: %w", err)
	}
	archive, err := root.Open(snapshotKey(digest))
	if err != nil {
		return fmt.Errorf("open local snapshot cache: %w", err)
	}
	defer archive.Close()
	_, err = s.hangar.Ensure(
		ctx,
		hangar.KindSnapshot,
		hangar.Digest("sha256:"+digest),
		archive,
		s.snapshotMaxBytes,
	)
	if err != nil {
		return fmt.Errorf("ensure snapshot in hangar: %w", err)
	}
	return nil
}

// restoreSnapshotFromHangar fills an empty local cache from the verified,
// generation-pinned Hangar reader. The durable store verifies the complete
// compressed representation before it returns a reader; this function then
// independently verifies the canonical digest before publishing a local link.
func (s *Server) restoreSnapshotFromHangar(ctx context.Context, digest string) error {
	if s.hangar == nil {
		return hangar.ErrNotFound
	}
	s.snapshotCacheMu.Lock()
	defer s.snapshotCacheMu.Unlock()

	if err := os.MkdirAll(s.storagePath, 0755); err != nil {
		return err
	}
	root, err := os.OpenRoot(s.storagePath)
	if err != nil {
		return err
	}
	defer root.Close()
	key := snapshotKey(digest)
	if info, statErr := root.Lstat(key); statErr == nil && info.Mode().IsRegular() {
		return nil
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := s.ensureSnapshotNamespace(root); err != nil {
		return err
	}
	attrs, err := s.hangar.Inspect(ctx, hangar.KindSnapshot, hangar.Digest("sha256:"+digest), s.snapshotMaxBytes)
	if err != nil {
		return err
	}
	reader, _, err := s.hangar.Open(ctx, attrs.Ref, s.snapshotMaxBytes)
	if err != nil {
		return err
	}
	defer reader.Close()

	parent := path.Dir(key)
	tmpKey, temporary, err := createSnapshotTemp(root, parent)
	if err != nil {
		return err
	}
	tmpExists := true
	defer func() {
		_ = temporary.Close()
		if tmpExists {
			_ = root.Remove(tmpKey)
		}
	}()
	hash := sha256.New()
	if _, err := copySnapshotContext(ctx, io.MultiWriter(temporary, hash), reader, s.snapshotMaxBytes); err != nil {
		return err
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != digest {
		return fmt.Errorf("restored snapshot digest mismatch: got %s, want %s", actual, digest)
	}
	if err := temporary.Chmod(0644); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	exists, identical, err := compareSnapshot(ctx, root, key, tmpKey)
	if err != nil {
		return err
	}
	if exists {
		if !identical {
			return fmt.Errorf("local snapshot cache conflicts with durable snapshot")
		}
		return nil
	}
	if err := root.Link(tmpKey, key); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := root.Remove(tmpKey); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmpExists = false
	return s.syncSnapshotDirectory(root, parent)
}

func (s *Server) handleGetSnapshot(w http.ResponseWriter, r *http.Request, digest string) {
	start := time.Now()
	status := "error"
	var copied int64
	defer func() { s.metrics.recordSnapshot("get", status, copied, time.Since(start)) }()
	release := s.guard.BeginRead(snapshotKey(digest))
	defer release()
	root, file, info, ok := s.openSnapshotForRead(r.Context(), w, digest)
	if !ok {
		return
	}
	defer root.Close()
	defer file.Close()
	setSnapshotHeaders(w, digest, info.Size())
	w.WriteHeader(http.StatusOK)
	copied, err := io.Copy(w, file)
	if err != nil || copied != info.Size() {
		panic(http.ErrAbortHandler)
	}
	status = "ok"
}

func (s *Server) handleHeadSnapshot(w http.ResponseWriter, r *http.Request, digest string) {
	start := time.Now()
	status := "error"
	defer func() { s.metrics.recordSnapshot("head", status, 0, time.Since(start)) }()
	release := s.guard.BeginRead(snapshotKey(digest))
	defer release()
	root, file, info, ok := s.openSnapshotForRead(r.Context(), w, digest)
	if !ok {
		return
	}
	defer root.Close()
	defer file.Close()
	setSnapshotHeaders(w, digest, info.Size())
	status = "ok"
	w.WriteHeader(http.StatusOK)
}

func (s *Server) openSnapshotForRead(ctx context.Context, w http.ResponseWriter, digest string) (*os.Root, *os.File, os.FileInfo, bool) {
	root, err := os.OpenRoot(s.storagePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return nil, nil, nil, false
		}
		if s.restoreSnapshotForRead(ctx, w, digest) {
			return s.openSnapshotForRead(ctx, w, digest)
		}
		return nil, nil, nil, false
	}
	key := snapshotKey(digest)
	info, err := root.Lstat(key)
	if err != nil {
		root.Close()
		if errors.Is(err, os.ErrNotExist) {
			if s.restoreSnapshotForRead(ctx, w, digest) {
				return s.openSnapshotForRead(ctx, w, digest)
			}
			return nil, nil, nil, false
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return nil, nil, nil, false
	}
	if !info.Mode().IsRegular() {
		root.Close()
		http.Error(w, "snapshot content conflict", http.StatusConflict)
		return nil, nil, nil, false
	}
	file, err := root.Open(key)
	if err != nil {
		root.Close()
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, nil, nil, false
	}
	if err := s.ensureSnapshotInHangar(ctx, root, digest); err != nil {
		file.Close()
		root.Close()
		s.logger.Error("adopt-snapshot-into-hangar-failed", err)
		http.Error(w, "durable snapshot upload failed", http.StatusBadGateway)
		return nil, nil, nil, false
	}
	return root, file, info, true
}

func (s *Server) restoreSnapshotForRead(ctx context.Context, w http.ResponseWriter, digest string) bool {
	if s.hangar == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	if err := s.restoreSnapshotFromHangar(ctx, digest); err == nil {
		return true
	} else if errors.Is(err, hangar.ErrNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
	} else {
		s.logger.Error("restore-snapshot-from-hangar-failed", err)
		http.Error(w, "durable snapshot restore failed", http.StatusBadGateway)
	}
	return false
}

func setSnapshotHeaders(w http.ResponseWriter, digest string, size int64) {
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", `"sha256:`+digest+`"`)
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request, digest string) {
	start := time.Now()
	status := "error"
	defer func() { s.metrics.recordSnapshot("delete", status, 0, time.Since(start)) }()
	if err := os.MkdirAll(s.storagePath, 0755); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	root, err := os.OpenRoot(s.storagePath)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	key := snapshotKey(digest)
	parent := path.Dir(key)
	cacheOnly := r.Header.Get(snapshotCacheOnlyDeleteHeader) == "true"
	if err := s.ensureSnapshotNamespace(root); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	release := s.guard.BeginSweep(key)
	defer release()
	info, err := root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		if !cacheOnly {
			if err := s.deleteSnapshotFromHangar(r.Context(), digest); err != nil {
				s.logger.Error("delete-snapshot-from-hangar-failed", err)
				http.Error(w, "durable snapshot deletion failed", http.StatusBadGateway)
				return
			}
		}
		if err := s.syncSnapshotDirectory(root, parent); err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		status = "not_found"
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !info.Mode().IsRegular() {
		status = "conflict"
		http.Error(w, "snapshot content conflict", http.StatusConflict)
		return
	}
	if !cacheOnly {
		if err := s.deleteSnapshotFromHangar(r.Context(), digest); err != nil {
			s.logger.Error("delete-snapshot-from-hangar-failed", err)
			http.Error(w, "durable snapshot deletion failed", http.StatusBadGateway)
			return
		}
	}
	if err := root.Remove(key); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := s.syncSnapshotDirectory(root, parent); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	status = "ok"
	w.WriteHeader(http.StatusNoContent)
}

// handleRepairSnapshotDurableMetadata restores the durable object's metadata
// vocabulary on behalf of the ATC's snapshot repair pass. Only the durable
// store can do this — it holds the object — but the decision to attempt it
// belongs upstream, in the component whose job is fixing snapshot state, so
// this is exposed as an explicit request and never runs off an ordinary read.
//
// Proving an object requires decompressing it, so the daemon admits a bounded
// number at a time and answers 429 rather than letting a repair pass turn into
// unbounded background work.
func (s *Server) handleRepairSnapshotDurableMetadata(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	status := "error"
	defer func() { s.metrics.recordSnapshot("repair-metadata", status, 0, time.Since(start)) }()

	digest := r.PathValue("digest")
	if _, err := snapshotDigestFromKey(snapshotKey(digest)); err != nil {
		http.Error(w, "malformed snapshot digest", http.StatusBadRequest)
		return
	}
	if s.hangar == nil {
		status = "not_found"
		http.Error(w, "durable snapshot store is not configured", http.StatusNotFound)
		return
	}

	select {
	case s.snapshotRepairSlots <- struct{}{}:
		defer func() { <-s.snapshotRepairSlots }()
	default:
		status = "busy"
		http.Error(w, "durable snapshot metadata repair is busy", http.StatusTooManyRequests)
		return
	}

	attributes, err := s.hangar.RepairDerivableMetadata(
		r.Context(),
		hangar.KindSnapshot,
		hangar.Digest("sha256:"+digest),
		s.snapshotMaxBytes,
	)
	switch {
	case err == nil:
	case errors.Is(err, hangar.ErrNotFound):
		status = "not_found"
		http.Error(w, "not found", http.StatusNotFound)
		return
	case errors.Is(err, hangar.ErrConflict):
		status = "conflict"
		http.Error(w, "durable snapshot changed during metadata repair", http.StatusConflict)
		return
	case errors.Is(err, hangar.ErrCorrupt), errors.Is(err, hangar.ErrUnrepairable):
		// The stored bytes did not prove themselves against the digest in the
		// key, or the object carries metadata this build cannot derive. Either
		// way the object is left exactly as it is and a human is needed; saying
		// so with a terminal status keeps the repair pass from thrashing.
		status = "unrepairable"
		s.logger.Error("repair-snapshot-durable-metadata-refused", err, lager.Data{"digest": digest})
		http.Error(w, "durable snapshot metadata is not repairable", http.StatusUnprocessableEntity)
		return
	default:
		s.logger.Error("repair-snapshot-durable-metadata-failed", err, lager.Data{"digest": digest})
		http.Error(w, "durable snapshot metadata repair failed", http.StatusBadGateway)
		return
	}

	status = "ok"
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"digest":             string(attributes.Ref.Digest),
		"uncompressed_bytes": attributes.UncompressedBytes,
	})
}

func (s *Server) deleteSnapshotFromHangar(ctx context.Context, digest string) error {
	if s.hangar == nil {
		return nil
	}
	attrs, err := s.hangar.Inspect(ctx, hangar.KindSnapshot, hangar.Digest("sha256:"+digest), s.snapshotMaxBytes)
	if errors.Is(err, hangar.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.hangar.Delete(ctx, attrs.Ref)
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	artifactPath, err := s.artifactPath(r)
	if err != nil {
		http.Error(w, "malformed artifact path", http.StatusBadRequest)
		return
	}
	digest, snapshotRequest, err := snapshotDigestForRequest(r)
	if err != nil {
		http.Error(w, "malformed snapshot path", http.StatusBadRequest)
		return
	}
	if snapshotRequest {
		s.handleGetSnapshot(w, r, digest)
		return
	}
	path := artifactPath

	// Check filesystem first, then fall back to registry aliases.
	// This enables peer daemons to serve registry-only artifacts
	// (e.g., resource caches registered via POST /register).
	file, info, release, err := s.openGenericArtifactWithReadGuard(r.Context(), path)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		// Filesystem miss — try registry lookup.
		file, info, path, release, err = s.openRegistryAliasForRequestWithReadGuard(r.Context(), r)
	}
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	defer release()

	// The read guard was acquired before opening the descriptor. Holding it
	// through the stream prevents stream-in/sweep from clearing the opened inode
	// in the gap and producing a valid-looking partial tar from a stale tree.
	if s.serveHooks.sourceDescriptorOpened != nil {
		s.serveHooks.sourceDescriptorOpened()
	}
	if s.serveHooks.sourceOpened != nil {
		s.serveHooks.sourceOpened()
	}
	s.touchStepDir(path)

	// Directory: tar on-the-fly and stream.
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		if err := tarOpenedDirectory(w, file); err != nil {
			s.logger.Error("failed-to-tar-artifact", err, lager.Data{"path": path})
			// The 200 header and part of the body are already out; abort
			// the connection so the client sees a hard failure instead of
			// a clean-looking truncated tar.
			panic(http.ErrAbortHandler)
		}
		return
	}

	// File: serve as-is (backward compat for legacy tar files).
	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, file); err != nil {
		s.logger.Error("failed-to-stream-artifact", err, lager.Data{"path": path})
		panic(http.ErrAbortHandler)
	}
}

// touchStepDir bumps the mtime of the steps/{handle} directory containing
// path (when path is under steps/) so the TTL sweeper treats actively-read
// artifacts as fresh. Best-effort.
func (s *Server) touchStepDir(path string) {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	rel, err := filepath.Rel(stepsRoot, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return
	}
	handle := strings.Split(rel, string(filepath.Separator))[0]
	now := time.Now()
	_ = os.Chtimes(filepath.Join(stepsRoot, handle), now, now)
}

func (s *Server) handlePutArtifact(w http.ResponseWriter, r *http.Request) {
	artifactPath, err := s.artifactPath(r)
	if err != nil {
		http.Error(w, "malformed artifact path", http.StatusBadRequest)
		return
	}
	digest, snapshotRequest, err := snapshotDigestForRequest(r)
	if err != nil {
		http.Error(w, "malformed snapshot path", http.StatusBadRequest)
		return
	}
	if snapshotRequest {
		s.handlePutSnapshot(w, r, digest)
		return
	}
	path := artifactPath
	rel, err := pathBelow(s.storagePath, path)
	if err != nil {
		http.Error(w, "malformed artifact path", http.StatusBadRequest)
		return
	}
	root, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	parentRel := filepath.Dir(rel)
	parent, err := openDirAtNoFollow(root, parentRel, true)
	if err != nil {
		http.Error(w, "invalid artifact destination", http.StatusBadRequest)
		return
	}
	defer parent.Close()
	if s.mutationHooks.destinationParentOpened != nil {
		s.mutationHooks.destinationParentOpened()
	}
	staging, err := openDaemonStagingAt(root)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer staging.Close()
	tmpName, file, err := randomFileAt(staging, ".put-tmp-", 0600)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if s.mutationHooks.temporaryOpened != nil {
		s.mutationHooks.temporaryOpened(staging, tmpName)
	}
	tmpExists := true
	defer func() {
		_ = file.Close()
		if tmpExists {
			_ = unix.Unlinkat(int(staging.Fd()), tmpName, 0)
		}
	}()
	if _, err := io.Copy(file, r.Body); err != nil {
		s.logger.Error("failed-to-write-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := unix.Fchmod(int(file.Fd()), 0644); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	temporaryUnchanged, err := sameOpenEntryAt(staging, tmpName, file)
	if err != nil || !temporaryUnchanged {
		http.Error(w, "artifact upload temporary changed before publication", http.StatusConflict)
		return
	}
	unchanged, err := sameOpenDirectoryAt(root, parentRel, parent)
	if err != nil || !unchanged {
		http.Error(w, "artifact destination changed during upload", http.StatusConflict)
		return
	}
	if err := unix.Renameat(int(staging.Fd()), tmpName, int(parent.Fd()), filepath.Base(rel)); err != nil {
		s.logger.Error("failed-to-publish-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpExists = false
	w.WriteHeader(http.StatusCreated)
}

// handleStreamIn accepts a tar stream (optionally gzip-compressed) and extracts
// it to steps/{key}/ so that resolveOne can discover it via the filesystem
// fallback. The key is also registered in the in-memory registry for fast lookups.
//
// Gzip is auto-detected by peeking at the first two bytes for the gzip magic
// number (\x1f\x8b). This allows both raw tar (from DaemonSetVolume.StreamIn)
// and gzipped tar (from fly CLI uploads) to work.
func (s *Server) handleStreamIn(w http.ResponseWriter, r *http.Request) {
	key, err := canonicalRequestKey(r.URL.Path, r.URL.EscapedPath(), "/stream-in/")
	if err != nil || snapshotNamespaceKey(key) {
		http.Error(w, "malformed stream-in key", http.StatusBadRequest)
		return
	}

	dest := filepath.Join(s.storagePath, "steps", key)

	// Extract into a temp sibling and rename into place on success. Readers
	// (resolve, GET/HEAD, peer probes) treat any existing steps/{key} dir as
	// a complete artifact, so partial state — an in-flight extraction or a
	// failed upload — must never be visible at the final path.
	stepsRoot := filepath.Join(s.storagePath, "steps")
	storageHandle, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		http.Error(w, "anchor storage root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer storageHandle.Close()
	if err := unix.Mkdirat(int(storageHandle.Fd()), "steps", 0755); err != nil && !errors.Is(err, unix.EEXIST) {
		http.Error(w, "create steps root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stepsHandle, err := openDirAtNoFollow(storageHandle, "steps", false)
	if err != nil {
		http.Error(w, "anchor steps root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer stepsHandle.Close()
	destRel := filepath.FromSlash(key)
	destParentRel := filepath.Dir(destRel)
	destParent, err := openDirAtNoFollow(stepsHandle, destParentRel, true)
	if err != nil {
		http.Error(w, "unsafe stream-in destination", http.StatusBadRequest)
		return
	}
	defer destParent.Close()
	tmpName, tmpHandle, err := randomDirectoryAt(stepsHandle, ".in-tmp-")
	if err != nil {
		s.logger.Error("failed-to-create-stream-in-tmp-dir", err, lager.Data{"key": key})
		http.Error(w, "create tmp dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpDest := filepath.Join(stepsRoot, tmpName)
	// No-op after the successful rename; cleans up on every error path.
	defer removeTreeAt(stepsHandle, tmpName)
	defer tmpHandle.Close()

	// Auto-detect gzip by peeking at the first 2 bytes.
	br := bufio.NewReader(r.Body)
	var tarSource io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		gr, err := gzip.NewReader(br)
		if err != nil {
			s.logger.Error("failed-to-open-gzip", err, lager.Data{"key": key})
			http.Error(w, "gzip: "+err.Error(), http.StatusBadRequest)
			return
		}
		defer gr.Close()
		tarSource = gr
	}

	extractionRoot, err := openRootAt(tmpHandle)
	if err != nil {
		http.Error(w, "open extraction root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	extractErr := extractTarAnchored(r.Context(), extractionRoot, tarSource)
	closeErr := extractionRoot.Close()
	if extractErr != nil || closeErr != nil {
		err := errors.Join(extractErr, closeErr)
		s.logger.Error("failed-to-extract-tar", err, lager.Data{"key": key})
		http.Error(w, "tar: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Move the fully-extracted tree into place. Remove any previous copy
	// first (rename onto a non-empty dir fails). The replace is destructive
	// like a sweep: take the handle's exclusive lock so in-flight reads
	// (resolve copies, GET/mirror tar walks) never see a half-removed tree.
	renameErr := func() error {
		release := s.guard.BeginSweep(s.stepHandle(dest))
		defer release()
		temporaryUnchanged, err := sameOpenDirectoryAt(stepsHandle, tmpName, tmpHandle)
		if err != nil {
			return fmt.Errorf("revalidate stream-in temporary directory: %w", err)
		}
		if !temporaryUnchanged {
			return fmt.Errorf("stream-in temporary directory changed before publication")
		}
		unchanged, err := sameOpenDirectoryAt(stepsHandle, destParentRel, destParent)
		if err != nil || !unchanged {
			return fmt.Errorf("stream-in destination parent changed: %w", err)
		}
		destBase := filepath.Base(destRel)
		if err := removeTreeAt(destParent, destBase); err != nil {
			return fmt.Errorf("remove stale stream-in destination: %w", err)
		}
		if s.mutationHooks.streamInDestinationRemoved != nil {
			s.mutationHooks.streamInDestinationRemoved()
		}
		return unix.Renameat(int(stepsHandle.Fd()), tmpName, int(destParent.Fd()), destBase)
	}()
	if renameErr != nil {
		s.logger.Error("failed-to-rename-stream-in", renameErr, lager.Data{"key": key, "tmp": tmpDest})
		http.Error(w, "rename: "+renameErr.Error(), http.StatusInternalServerError)
		return
	}

	s.registry.Register(key, dest)
	s.logger.Info("stream-in-complete", lager.Data{"key": key, "dest": dest})

	// Schedule outbound mirror to peer daemons so the new artifact survives
	// loss of this node. Best-effort: the trigger queues a background job
	// and returns immediately; mirror is disabled by passing a nil trigger.
	// Writes that themselves arrived via a peer's mirror must NOT re-trigger
	// (see MirrorOriginHeader) — the origin daemon already fanned out, and
	// re-mirroring makes daemons ping-pong the same key indefinitely.
	if s.mirrorTrigger != nil && r.Header.Get(MirrorOriginHeader) == "" {
		s.mirrorTrigger(r.Context(), key)
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	artifactPath, err := s.artifactPath(r)
	if err != nil {
		http.Error(w, "malformed artifact path", http.StatusBadRequest)
		return
	}
	digest, snapshotRequest, err := snapshotDigestForRequest(r)
	if err != nil {
		http.Error(w, "malformed snapshot path", http.StatusBadRequest)
		return
	}
	if snapshotRequest {
		s.handleDeleteSnapshot(w, r, digest)
		return
	}
	path := artifactPath
	rel, err := pathBelow(s.storagePath, path)
	if err != nil {
		http.Error(w, "malformed artifact path", http.StatusBadRequest)
		return
	}
	release := s.guard.BeginSweep(s.stepHandle(path))
	defer release()
	root, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer root.Close()
	parentRel := filepath.Dir(rel)
	parent, err := openDirAtNoFollow(root, parentRel, false)
	if errors.Is(err, os.ErrNotExist) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, "invalid artifact target", http.StatusBadRequest)
		return
	}
	defer parent.Close()
	if s.mutationHooks.destinationParentOpened != nil {
		s.mutationHooks.destinationParentOpened()
	}
	unchanged, err := sameOpenDirectoryAt(root, parentRel, parent)
	if err != nil || !unchanged {
		http.Error(w, "artifact target changed during delete", http.StatusConflict)
		return
	}
	if err := removeTreeAt(parent, filepath.Base(rel)); err != nil {
		s.logger.Error("failed-to-delete-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeadArtifact(w http.ResponseWriter, r *http.Request) {
	artifactPath, err := s.artifactPath(r)
	if err != nil {
		http.Error(w, "malformed artifact path", http.StatusBadRequest)
		return
	}
	digest, snapshotRequest, err := snapshotDigestForRequest(r)
	if err != nil {
		http.Error(w, "malformed snapshot path", http.StatusBadRequest)
		return
	}
	if snapshotRequest {
		s.handleHeadSnapshot(w, r, digest)
		return
	}
	path := artifactPath

	file, _, release, err := s.openGenericArtifactWithReadGuard(r.Context(), path)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		file, _, _, release, err = s.openRegistryAliasForRequestWithReadGuard(r.Context(), r)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"path": path})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	file.Close()
	release()
	w.WriteHeader(http.StatusOK)
}

// registerRequest is the JSON body for POST /register.
type registerRequest struct {
	Key       string `json:"key"`
	LocalPath string `json:"local_path"`
}

// mirrorRequest is the JSON body for POST /mirror.
type mirrorRequest struct {
	Key string `json:"key"`
}

// handleMirrorTrigger accepts POST /mirror with a JSON body containing
// {"key": "handle/output"} and schedules an asynchronous mirror of the
// local steps/{key}/ directory to peer daemons. Returns 202 Accepted
// immediately — the caller should not wait on the mirror to complete.
//
// The mirror trigger function is set by SetMirrorTrigger; if unset (mirror
// disabled), the endpoint still accepts the request and returns 202 so
// that ATC callers don't fail when talking to a daemon that has mirror
// off.
func (s *Server) handleMirrorTrigger(w http.ResponseWriter, r *http.Request) {
	var req mirrorRequest
	if !decodeControlJSON(w, r, &req, "mirror") {
		return
	}
	if err := validateCanonicalRelativeKey(req.Key); err != nil || snapshotNamespaceKey(req.Key) {
		http.Error(w, "invalid mirror key", http.StatusBadRequest)
		return
	}

	if s.mirrorTrigger != nil {
		s.mirrorTrigger(r.Context(), req.Key)
	}

	w.WriteHeader(http.StatusAccepted)
}

// resolveRequest is the JSON body for POST /resolve.
type resolveRequest struct {
	Key        string `json:"key"`
	Dest       string `json:"dest"`
	Capability string `json:"capability,omitempty"`
}

const (
	maxResolveControlBody = 64 << 10
	maxResolveBatchItems  = 64
	maxResolveWorkers     = 8
)

func decodeResolveControlJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	return decodeControlJSON(w, r, destination, "resolve")
}

func decodeControlJSON(w http.ResponseWriter, r *http.Request, destination any, kind string) bool {
	if r.ContentLength > maxResolveControlBody {
		http.Error(w, kind+" request body is too large", http.StatusRequestEntityTooLarge)
		return false
	}
	limited := http.MaxBytesReader(w, r.Body, maxResolveControlBody)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, kind+" request body is too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid "+kind+" request", http.StatusBadRequest)
		}
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, kind+" request must contain exactly one JSON value", http.StatusBadRequest)
		return false
	}
	return true
}

// resolveResponse is the JSON body returned by POST /resolve.
type resolveResponse struct {
	Status          string `json:"status"`
	Method          string `json:"method"`
	Duration        string `json:"duration,omitempty"`
	Error           string `json:"error,omitempty"`
	Acknowledgement string `json:"acknowledgement,omitempty"`
}

// handleRegister accepts POST /register with a JSON body containing
// {key, local_path} and registers the artifact in the daemon's registry.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeControlJSON(w, r, &req, "register") {
		return
	}
	if req.Key == "" || req.LocalPath == "" {
		http.Error(w, "key and local_path are required", http.StatusBadRequest)
		return
	}
	if err := validateCanonicalRelativeKey(req.Key); err != nil || snapshotNamespaceKey(req.Key) {
		http.Error(w, "invalid registration key", http.StatusBadRequest)
		return
	}

	if _, err := s.validateStepPath(req.LocalPath, false); err != nil {
		if _, statErr := os.Lstat(req.LocalPath); errors.Is(statErr, os.ErrNotExist) {
			http.Error(w, fmt.Sprintf("path not found: %s", req.LocalPath), http.StatusNotFound)
			return
		}
		http.Error(w, "registration source must be beneath the steps root", http.StatusBadRequest)
		return
	}
	file, _, err := s.openGenericArtifact(req.LocalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.logger.Info("register-path-not-found", lager.Data{"key": req.Key, "path": req.LocalPath})
			http.Error(w, fmt.Sprintf("path not found: %s", req.LocalPath), http.StatusNotFound)
			return
		}
		s.logger.Error("register-stat-error", err, lager.Data{"key": req.Key, "path": req.LocalPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	file.Close()

	s.registry.RegisterAlias(req.Key, req.LocalPath)

	s.logger.Info("registered", lager.Data{"key": req.Key, "path": req.LocalPath})
	w.WriteHeader(http.StatusCreated)
}

var errResolveRegistryChanged = errors.New("registry mapping changed during guarded resolve")

// copyRegisteredArtifactGuarded confirms the expected registry mapping only
// after acquiring the paired source/destination guard, then validates, opens,
// and copies the source without releasing that guard. This ordering prevents a
// stream-in publication gap from looking like a stale alias.
func (s *Server) copyRegisteredArtifactGuarded(ctx context.Context, key, expectedSource, dest, acknowledgement string) (bool, error) {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	if _, err := pathBelow(stepsRoot, expectedSource); err != nil {
		s.registry.RemoveIf(key, expectedSource)
		return true, fmt.Errorf("unsafe registry source: %w", err)
	}

	release, err := s.guard.BeginResolveContext(ctx, s.stepHandle(expectedSource), s.stepHandle(dest))
	if err != nil {
		return false, err
	}
	defer release()

	current, stillRegistered := s.registry.Lookup(key)
	if !stillRegistered || current != expectedSource {
		return false, errResolveRegistryChanged
	}
	if _, err := s.validateStepPath(expectedSource, true); err != nil {
		s.registry.RemoveIf(key, expectedSource)
		return true, fmt.Errorf("unsafe registry source: %w", err)
	}
	opened, info, err := s.openGenericArtifact(expectedSource)
	if err != nil {
		s.registry.RemoveIf(key, expectedSource)
		return true, fmt.Errorf("registry source is not anchored: %w", err)
	}
	opened.Close()
	if !info.IsDir() {
		s.registry.RemoveIf(key, expectedSource)
		return true, fmt.Errorf("registry source is not an anchored directory")
	}

	s.touchStepDir(expectedSource)
	return false, s.copyArtifactContextWithReceipt(ctx, expectedSource, dest, acknowledgement)
}

// copyFilesystemArtifactGuarded checks and copies the canonical steps/{key}
// fallback while holding the same paired guard used for publication. A
// registry entry that appears while waiting wins and makes the caller retry
// the registered path instead.
func (s *Server) copyFilesystemArtifactGuarded(ctx context.Context, key, sourcePath, dest, acknowledgement string) (bool, error) {
	release, err := s.guard.BeginResolveContext(ctx, s.stepHandle(sourcePath), s.stepHandle(dest))
	if err != nil {
		return false, err
	}
	defer release()

	if _, registered := s.registry.Lookup(key); registered {
		return false, errResolveRegistryChanged
	}
	file, info, err := s.openGenericArtifact(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open filesystem source: %w", err)
	}
	file.Close()
	if !info.IsDir() {
		return false, nil
	}
	if !s.registry.RegisterIfAbsent(key, sourcePath) {
		return false, errResolveRegistryChanged
	}

	s.touchStepDir(sourcePath)
	return true, s.copyArtifactContextWithReceipt(ctx, sourcePath, dest, acknowledgement)
}

// resolveOne resolves a single artifact key to a destination path.
// It is the core logic shared by handleResolve and handleResolveBatch.
func (s *Server) resolveOne(ctx context.Context, key, dest, acknowledgement string) (resp resolveResponse) {
	start := time.Now()
	defer func() {
		s.metrics.recordResolve(resp.Method, resp.Status, time.Since(start))
	}()
	logger := s.logger.Session("resolve", lager.Data{"key": key, "dest": dest})
	if err := s.validateResolveBoundary(key, dest); err != nil {
		return resolveResponse{Status: "error", Method: "validation", Error: err.Error()}
	}
	if err := ctx.Err(); err != nil {
		return resolveFailure("admission", err)
	}
	select {
	case s.resolveSlots <- struct{}{}:
		defer func() { <-s.resolveSlots }()
	default:
		return resolveResponse{Status: "busy", Method: "admission", Error: "artifact resolver is at its daemon-wide concurrency limit"}
	}
	resolveCtx, cancel := context.WithTimeout(ctx, s.resolveTimeout)
	defer cancel()
	ctx = resolveCtx

	// Steps 1 and 2: resolve a registered source or the canonical filesystem
	// fallback. Mapping changes are retried so lookup, source open, and copy all
	// agree under the paired guard.
	stepsPath := filepath.Join(s.storagePath, "steps", key)
	localSearchSettled := false
	for range 8 {
		sourcePath, found := s.registry.Lookup(key)
		if found {
			invalid, err := s.copyRegisteredArtifactGuarded(ctx, key, sourcePath, dest, acknowledgement)
			if errors.Is(err, errResolveRegistryChanged) {
				continue
			}
			if err != nil {
				logger.Error("copy-failed", err, lager.Data{"source": sourcePath})
				if invalid {
					return resolveResponse{Status: "error", Method: "validation", Error: err.Error()}
				}
				return resolveFailure("local", err)
			}
			duration := time.Since(start)
			logger.Info("resolved", lager.Data{"method": "registry", "source": sourcePath, "duration": duration.String()})
			return resolveResponse{Status: "ok", Method: "registry", Duration: duration.String(), Acknowledgement: acknowledgement}
		}

		copied, err := s.copyFilesystemArtifactGuarded(ctx, key, stepsPath, dest, acknowledgement)
		if errors.Is(err, errResolveRegistryChanged) {
			continue
		}
		if err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": stepsPath})
			return resolveFailure("filesystem", err)
		}
		if copied {
			duration := time.Since(start)
			logger.Info("resolved", lager.Data{"method": "filesystem", "source": stepsPath, "duration": duration.String()})
			return resolveResponse{Status: "ok", Method: "filesystem", Duration: duration.String(), Acknowledgement: acknowledgement}
		}
		localSearchSettled = true
		break
	}
	if !localSearchSettled {
		return resolveResponse{Status: "error", Method: "validation", Error: "registry mapping changed repeatedly during guarded resolve"}
	}

	// Step 3: Query peer daemons for cross-node resolution.
	if s.peers != nil {
		peerIP, found := s.peers.Probe(ctx, key)
		if found {
			if err := s.fetchPeerArtifact(ctx, peerIP, key, dest, acknowledgement); err != nil {
				logger.Error("peer-fetch-failed", err, lager.Data{"peer": peerIP})
				return resolveFailure("peer", err)
			}
			duration := time.Since(start)
			logger.Info("resolved", lager.Data{"method": "peer", "peer": peerIP, "duration": duration.String()})
			return resolveResponse{Status: "ok", Method: "peer", Duration: duration.String(), Acknowledgement: acknowledgement}
		}
		if err := ctx.Err(); err != nil {
			return resolveFailure("peer", err)
		}
	}

	// Step 4: Not found anywhere.
	duration := time.Since(start)
	logger.Info("not-found", lager.Data{"duration": duration.String()})
	return resolveResponse{Status: "not_found", Method: "exhausted", Duration: duration.String(), Error: fmt.Sprintf("artifact %q not found on this node or any peer", key)}
}

func resolveFailure(method string, err error) resolveResponse {
	status := "error"
	if errors.Is(err, context.DeadlineExceeded) {
		status = "timeout"
	}
	return resolveResponse{Status: status, Method: method, Error: err.Error()}
}

func (s *Server) validateSnapshotResolveBoundary(key, dest string) (string, error) {
	digest, err := snapshotDigestFromKey(key)
	if err != nil {
		return "", fmt.Errorf("invalid snapshot source key: %w", err)
	}
	if _, err := s.validateStepPath(dest, false); err != nil {
		return "", fmt.Errorf("invalid destination: %w", err)
	}
	return digest, nil
}

// resolveSnapshotOne materializes one immutable canonical snapshot archive
// into an ordinary step input. The signed source is the digest namespace key,
// never the mutable semantic snapshot ID. Bytes are hash-verified before the
// prepared destination tree becomes visible to the kubelet bind mount.
func (s *Server) resolveSnapshotOne(ctx context.Context, key, digest, dest, acknowledgement string) (resp resolveResponse) {
	start := time.Now()
	defer func() {
		s.metrics.recordResolve(resp.Method, resp.Status, time.Since(start))
	}()
	logger := s.logger.Session("resolve-snapshot", lager.Data{"key": key, "dest": dest})
	validatedDigest, err := s.validateSnapshotResolveBoundary(key, dest)
	if err != nil || validatedDigest != digest {
		if err == nil {
			err = fmt.Errorf("snapshot digest classification changed")
		}
		return resolveFailure("snapshot-validation", err)
	}
	if err := ctx.Err(); err != nil {
		return resolveFailure("snapshot-admission", err)
	}
	select {
	case s.resolveSlots <- struct{}{}:
		defer func() { <-s.resolveSlots }()
	default:
		return resolveResponse{Status: "busy", Method: "snapshot-admission", Error: "artifact resolver is at its daemon-wide concurrency limit"}
	}
	resolveCtx, cancel := context.WithTimeout(ctx, s.resolveTimeout)
	defer cancel()

	foundLocal, localErr := s.materializeLocalSnapshot(resolveCtx, digest, dest, acknowledgement)
	if foundLocal && localErr == nil {
		duration := time.Since(start)
		return resolveResponse{Status: "ok", Method: "snapshot-local", Duration: duration.String(), Acknowledgement: acknowledgement}
	}
	if localErr != nil {
		logger.Error("local-snapshot-invalid", localErr)
	}

	if s.peers != nil {
		var peerErrors []error
		for _, peerIP := range s.peers.SnapshotPeers(resolveCtx, digest) {
			err := s.peers.FetchSnapshot(resolveCtx, peerIP, digest, func(reader io.Reader) error {
				return s.materializeSnapshotReader(resolveCtx, reader, digest, dest, acknowledgement)
			})
			if err == nil {
				duration := time.Since(start)
				return resolveResponse{Status: "ok", Method: "snapshot-peer", Duration: duration.String(), Acknowledgement: acknowledgement}
			}
			logger.Error("peer-snapshot-fetch-failed", err, lager.Data{"peer": peerIP})
			peerErrors = append(peerErrors, fmt.Errorf("peer %s: %w", peerIP, err))
		}
		if len(peerErrors) > 0 {
			err := errors.Join(peerErrors...)
			if localErr != nil {
				err = errors.Join(localErr, err)
			}
			return resolveFailure("snapshot-peer", err)
		}
		if err := resolveCtx.Err(); err != nil {
			return resolveFailure("snapshot-peer", err)
		}
	}
	if localErr != nil {
		return resolveFailure("snapshot-local", localErr)
	}
	return resolveResponse{
		Status: "not_found", Method: "snapshot-exhausted",
		Duration: time.Since(start).String(), Error: fmt.Sprintf("snapshot %q not found on this node or any peer", digest),
	}
}

func (s *Server) materializeLocalSnapshot(ctx context.Context, digest, dest, acknowledgement string) (bool, error) {
	key := snapshotKey(digest)
	release := s.guard.BeginRead(key)
	defer release()
	root, err := os.OpenRoot(s.storagePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return true, fmt.Errorf("snapshot content is not a regular file")
	}
	file, err := root.Open(key)
	if err != nil {
		return true, err
	}
	defer file.Close()
	return true, s.materializeSnapshotReader(ctx, file, digest, dest, acknowledgement)
}

func (s *Server) materializeSnapshotReader(ctx context.Context, source io.Reader, digest, dest, acknowledgement string) error {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	destRel, err := pathBelow(stepsRoot, dest)
	if err != nil {
		return fmt.Errorf("derive snapshot destination: %w", err)
	}
	storageHandle, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		return fmt.Errorf("anchor snapshot storage root: %w", err)
	}
	defer storageHandle.Close()
	if err := unix.Mkdirat(int(storageHandle.Fd()), "steps", 0755); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("create snapshot steps root: %w", err)
	}
	stepsHandle, err := openDirAtNoFollow(storageHandle, "steps", false)
	if err != nil {
		return fmt.Errorf("anchor snapshot steps root: %w", err)
	}
	defer stepsHandle.Close()
	destParentRel := filepath.Dir(destRel)
	destParent, err := openDirAtNoFollow(stepsHandle, destParentRel, true)
	if err != nil {
		return fmt.Errorf("create snapshot destination parent: %w", err)
	}
	defer destParent.Close()
	release, err := s.guard.BeginSweepContext(ctx, s.stepHandle(dest))
	if err != nil {
		return err
	}
	defer release()
	unchanged, err := sameOpenDirectoryAt(stepsHandle, destParentRel, destParent)
	if err != nil || !unchanged {
		return fmt.Errorf("snapshot destination parent changed before extraction: %w", err)
	}
	receiptName, err := resolveReceiptMaterial(acknowledgement)
	if err != nil {
		return err
	}
	verifier := newSnapshotResolveVerifier(ctx, source, digest, s.snapshotMaxBytes)
	if err := extractTarIntoOpenedDirectoryWithReceiptAndVerify(
		ctx,
		verifier,
		destParent,
		filepath.Base(destRel),
		receiptName,
		[]byte(acknowledgement),
		verifier.Verify,
	); err != nil {
		return fmt.Errorf("extract verified snapshot: %w", err)
	}
	return nil
}

type snapshotResolveVerifier struct {
	ctx      context.Context
	reader   *io.LimitedReader
	hash     hash.Hash
	expected string
	maxBytes int64
	read     int64
}

func newSnapshotResolveVerifier(ctx context.Context, source io.Reader, expected string, maxBytes int64) *snapshotResolveVerifier {
	return &snapshotResolveVerifier{
		ctx: ctx, reader: &io.LimitedReader{R: source, N: maxBytes + 1}, hash: sha256.New(), expected: expected, maxBytes: maxBytes,
	}
}

func (reader *snapshotResolveVerifier) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := reader.reader.Read(buffer)
	if count > 0 {
		reader.read += int64(count)
		_, _ = reader.hash.Write(buffer[:count])
	}
	return count, err
}

func (reader *snapshotResolveVerifier) Verify() error {
	if _, err := io.Copy(io.Discard, reader); err != nil {
		return err
	}
	if reader.read > reader.maxBytes {
		return fmt.Errorf("snapshot exceeds maximum size")
	}
	actual := hex.EncodeToString(reader.hash.Sum(nil))
	if actual != reader.expected {
		return fmt.Errorf("snapshot digest mismatch: got %s, want %s", actual, reader.expected)
	}
	return nil
}

func (s *Server) fetchPeerArtifact(ctx context.Context, peerIP, key, dest, acknowledgement string) error {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	destRel, err := pathBelow(stepsRoot, dest)
	if err != nil {
		return fmt.Errorf("derive peer destination: %w", err)
	}
	storageHandle, err := openDirectoryNoFollow(s.storagePath)
	if err != nil {
		return fmt.Errorf("anchor peer storage root: %w", err)
	}
	defer storageHandle.Close()
	if err := unix.Mkdirat(int(storageHandle.Fd()), "steps", 0755); err != nil && !errors.Is(err, unix.EEXIST) {
		return fmt.Errorf("create peer steps root: %w", err)
	}
	stepsHandle, err := openDirAtNoFollow(storageHandle, "steps", false)
	if err != nil {
		return fmt.Errorf("anchor peer steps root: %w", err)
	}
	defer stepsHandle.Close()
	destParentRel := filepath.Dir(destRel)
	destParent, err := openDirAtNoFollow(stepsHandle, destParentRel, true)
	if err != nil {
		return fmt.Errorf("create peer destination parent: %w", err)
	}
	defer destParent.Close()
	release, err := s.guard.BeginSweepContext(ctx, s.stepHandle(dest))
	if err != nil {
		return err
	}
	defer release()
	unchanged, err := sameOpenDirectoryAt(stepsHandle, destParentRel, destParent)
	if err != nil {
		return fmt.Errorf("revalidate peer destination parent: %w", err)
	}
	if !unchanged {
		return fmt.Errorf("peer destination parent changed before fetch")
	}
	destBase := filepath.Base(destRel)
	receiptName, err := resolveReceiptMaterial(acknowledgement)
	if err != nil {
		return err
	}
	if err := s.peers.FetchIntoOpenedDirectoryWithReceipt(ctx, peerIP, key, destParent, destBase, receiptName, []byte(acknowledgement)); err != nil {
		return err
	}
	unchanged, err = sameOpenDirectoryAt(stepsHandle, destParentRel, destParent)
	if err != nil {
		cleanupErr := removeTreeAt(destParent, destBase)
		return fmt.Errorf("revalidate peer destination parent after fetch: %w", errors.Join(err, cleanupErr))
	}
	if !unchanged {
		cleanupErr := removeTreeAt(destParent, destBase)
		return errors.Join(fmt.Errorf("peer destination parent changed during fetch"), cleanupErr)
	}
	return nil
}

// handleResolve accepts POST /resolve with a JSON body containing {key, dest}.
// It looks up the artifact by key and copies it to the destination path.
//
// Resolution order:
//  1. Check local registry for an explicit registration
//  2. Fall back to filesystem scan (check if the key maps to a steps/ directory)
//  3. Query peer daemons for cross-node resolution
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request, cfg handlerConfig) {
	var req resolveRequest
	if !decodeResolveControlJSON(w, r, &req) {
		return
	}
	if req.Key == "" || req.Dest == "" {
		http.Error(w, "key and dest are required", http.StatusBadRequest)
		return
	}
	if err := s.validateResolveBoundary(req.Key, req.Dest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	acknowledgement, authorized := authorizeResolve(cfg, req)
	if !authorized {
		http.Error(w, "valid resolve capability required", http.StatusUnauthorized)
		return
	}

	resp := s.resolveOne(r.Context(), req.Key, req.Dest, acknowledgement)

	status := http.StatusOK
	if resp.Status == "busy" {
		status = http.StatusServiceUnavailable
	} else if resp.Status == "timeout" {
		status = http.StatusGatewayTimeout
	} else if resp.Status == "error" {
		status = http.StatusInternalServerError
	} else if resp.Status == "not_found" {
		status = http.StatusNotFound
	}
	writeJSON(w, status, resp)
}

// batchResolveRequest is the JSON body for POST /resolve-batch.
type batchResolveRequest struct {
	Items []resolveRequest `json:"items"`
}

// batchResolveResponse is the JSON body returned by POST /resolve-batch.
type batchResolveResponse struct {
	Status  string            `json:"status"`
	Results []resolveResponse `json:"results"`
}

// handleResolveBatch accepts POST /resolve-batch with a JSON body containing
// {"items": [{key, dest}, ...]}. It resolves all artifacts concurrently and
// returns an aggregated response. If any item fails, the overall status is
// "error" and the HTTP status is 500.
func (s *Server) handleResolveBatch(w http.ResponseWriter, r *http.Request, cfg handlerConfig) {
	var req batchResolveRequest
	if !decodeResolveControlJSON(w, r, &req) {
		return
	}
	if len(req.Items) == 0 {
		http.Error(w, "batch requires at least one item", http.StatusBadRequest)
		return
	}
	if len(req.Items) > maxResolveBatchItems {
		http.Error(w, "resolve batch has too many items", http.StatusRequestEntityTooLarge)
		return
	}
	destinations := make([]string, 0, len(req.Items))
	operations := make(map[string]struct{}, len(req.Items))
	snapshotDigests := make([]string, len(req.Items))
	for index, item := range req.Items {
		if item.Key == "" || item.Dest == "" {
			http.Error(w, "every item requires key and dest", http.StatusBadRequest)
			return
		}
		if snapshotNamespaceKey(item.Key) {
			digest, err := s.validateSnapshotResolveBoundary(item.Key, item.Dest)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			snapshotDigests[index] = digest
		} else {
			if err := s.validateResolveBoundary(item.Key, item.Dest); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		operation := item.Key + "\x00" + item.Dest
		if _, exists := operations[operation]; exists {
			http.Error(w, "batch items must not be duplicated", http.StatusBadRequest)
			return
		}
		operations[operation] = struct{}{}
		for _, existing := range destinations {
			if pathsOverlap(existing, item.Dest) {
				http.Error(w, "batch destinations must not overlap", http.StatusBadRequest)
				return
			}
		}
		destinations = append(destinations, item.Dest)
	}
	acknowledgements := make([]string, len(req.Items))
	for index, item := range req.Items {
		acknowledgement, authorized := authorizeResolve(cfg, item)
		if !authorized {
			http.Error(w, "valid resolve capability required", http.StatusUnauthorized)
			return
		}
		acknowledgements[index] = acknowledgement
	}

	results := make([]resolveResponse, len(req.Items))
	jobs := make(chan int)
	var wg sync.WaitGroup
	workerCount := min(maxResolveWorkers, len(req.Items))
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				item := req.Items[idx]
				if snapshotDigests[idx] != "" {
					results[idx] = s.resolveSnapshotOne(
						r.Context(), item.Key, snapshotDigests[idx], item.Dest, acknowledgements[idx],
					)
				} else {
					results[idx] = s.resolveOne(r.Context(), item.Key, item.Dest, acknowledgements[idx])
				}
			}
		}()
	}
	for i := range req.Items {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	overall := "ok"
	for _, res := range results {
		if res.Status != "ok" {
			overall = "error"
			break
		}
	}

	status := http.StatusOK
	if overall == "error" {
		status = http.StatusInternalServerError
		for _, result := range results {
			if result.Status == "busy" {
				status = http.StatusServiceUnavailable
				break
			}
			if result.Status == "timeout" {
				status = http.StatusGatewayTimeout
			}
		}
	}

	writeJSON(w, status, batchResolveResponse{Status: overall, Results: results})
}

func authorizeResolve(cfg handlerConfig, req resolveRequest) (string, bool) {
	if !cfg.resolveCapabilityRequired {
		return "", true
	}
	if cfg.resolveCapabilityVerifier == nil || req.Capability == "" {
		return "", false
	}
	if cfg.resolveCapabilityVerifier.VerifyResolve(req.Capability, req.Key, req.Dest, time.Now()) != nil {
		return "", false
	}
	acknowledgement, err := cfg.resolveCapabilityVerifier.ResolveAcknowledgement(req.Capability)
	return acknowledgement, err == nil
}

// copyArtifact copies the contents of src directory to dest atomically.
// It copies into a temporary sibling directory first, then renames to the
// final path. This prevents partial state from blocking retries when a
// previous copy was interrupted (e.g., by restrictive or read-only files
// left in the destination).
func (s *Server) copyArtifact(src, dest string) error {
	return s.copyArtifactContext(context.Background(), src, dest)
}

func (s *Server) copyArtifactContext(ctx context.Context, src, dest string) error {
	return s.copyArtifactContextWithReceipt(ctx, src, dest, "")
}

func (s *Server) copyArtifactContextWithReceipt(ctx context.Context, src, dest, acknowledgement string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stepsRoot := filepath.Join(s.storagePath, "steps")
	srcRel, err := pathBelow(stepsRoot, src)
	if err != nil {
		return fmt.Errorf("derive source path: %w", err)
	}
	destRel, err := pathBelow(stepsRoot, dest)
	if err != nil {
		return fmt.Errorf("derive destination path: %w", err)
	}
	if pathsOverlap(srcRel, destRel) {
		return fmt.Errorf("source and destination must not overlap")
	}
	stepsHandle, err := openDirectoryNoFollow(stepsRoot)
	if err != nil {
		return fmt.Errorf("anchor steps root: %w", err)
	}
	defer stepsHandle.Close()
	sourceHandle, err := openDirAtNoFollow(stepsHandle, srcRel, false)
	if err != nil {
		return fmt.Errorf("open source without symlinks: %w", err)
	}
	defer sourceHandle.Close()
	if s.copyHooks.sourceOpened != nil {
		s.copyHooks.sourceOpened()
	}
	destParentRel := filepath.Dir(destRel)
	destParent, err := openDirAtNoFollow(stepsHandle, destParentRel, true)
	if err != nil {
		return fmt.Errorf("open destination parent without symlinks: %w", err)
	}
	defer destParent.Close()
	if s.copyHooks.destinationParentOpened != nil {
		s.copyHooks.destinationParentOpened()
	}
	tmpName, tmpHandle, err := randomDirectoryAt(stepsHandle, ".cp-tmp-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer removeTreeAt(stepsHandle, tmpName)
	defer tmpHandle.Close()
	if err := copyOpenedTree(ctx, sourceHandle, tmpHandle, ""); err != nil {
		return fmt.Errorf("copy opened artifact tree: %w", err)
	}
	receiptName, err := resolveReceiptMaterial(acknowledgement)
	if err != nil {
		return err
	}
	if receiptName != "" {
		// The expected acknowledgement is already embedded in the init script;
		// the receipt authenticates the node-local write but is not a secret.
		// Read-only world access lets a non-root helper consume it without
		// granting that helper ownership of the hostPath tree.
		if err := writeExclusiveFileAt(tmpHandle, receiptName, []byte(acknowledgement), 0444); err != nil {
			return fmt.Errorf("write resolve receipt: %w", err)
		}
	}
	if s.copyHooks.temporaryReady != nil {
		s.copyHooks.temporaryReady(tmpName)
	}
	temporaryUnchanged, err := sameOpenDirectoryAt(stepsHandle, tmpName, tmpHandle)
	if err != nil || !temporaryUnchanged {
		return fmt.Errorf("copied temporary tree changed before publication: %w", err)
	}
	unchanged, err := sameOpenDirectoryAt(stepsHandle, destParentRel, destParent)
	if err != nil || !unchanged {
		return fmt.Errorf("destination parent changed during copy: %w", err)
	}
	destBase := filepath.Base(destRel)
	if err := publishPreparedDirectoryAt(ctx, stepsHandle, tmpName, tmpHandle, destParent, destBase, receiptName, s.copyHooks.destinationEntryPublishing); err != nil {
		return fmt.Errorf("publish copied artifact: %w", err)
	}
	return nil
}

func resolveReceiptMaterial(acknowledgement string) (string, error) {
	if acknowledgement == "" {
		return "", nil
	}
	name, err := artifactcap.ResolveReceiptFilename(acknowledgement)
	if err != nil {
		return "", fmt.Errorf("derive resolve receipt: %w", err)
	}
	return name, nil
}

// sanitizeMode strips setuid/setgid bits and enforces a minimum permission
// floor so the daemon can always read artifacts it extracted. Directories get
// at least 0755 (traversable + listable), files get at least 0644 (readable).
func sanitizeMode(typeflag byte, mode os.FileMode) os.FileMode {
	mode &^= os.ModeSetuid | os.ModeSetgid
	switch typeflag {
	case tar.TypeDir:
		mode |= 0755
	case tar.TypeReg:
		mode |= 0644
	}
	return mode
}

// handleHeadResourceCache checks whether a resource cache key exists on this
// daemon. The key is looked up in the registry (registered as an alias after a
// successful get step). Returns 200 with X-Node-Name header if found, 404
// otherwise.
func (s *Server) handleHeadResourceCache(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/resource-caches/")
	if err := validateCanonicalRelativeKey(key); err != nil || snapshotNamespaceKey(key) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	file, _, path, release, err := s.openRegisteredArtifactWithReadGuard(r.Context(), key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "path": path})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	file.Close()
	release()

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}
	w.WriteHeader(http.StatusOK)
}

// handleGetResourceCache streams a resource cache as a tar archive. Used by
// peer daemons to fetch cached resource data for cross-node resolution.
func (s *Server) handleGetResourceCache(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/resource-caches/")
	if err := validateCanonicalRelativeKey(key); err != nil || snapshotNamespaceKey(key) {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}

	file, info, path, release, err := s.openRegisteredArtifactWithReadGuard(r.Context(), key)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	defer release()

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}

	if s.serveHooks.sourceDescriptorOpened != nil {
		s.serveHooks.sourceDescriptorOpened()
	}
	if s.serveHooks.sourceOpened != nil {
		s.serveHooks.sourceOpened()
	}
	s.touchStepDir(path)

	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		if err := tarOpenedDirectory(w, file); err != nil {
			s.logger.Error("failed-to-tar-resource-cache", err, lager.Data{"key": key, "path": path})
			// Headers already sent; abort so the peer sees a hard failure
			// rather than a clean-looking truncated tar.
			panic(http.ErrAbortHandler)
		}
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, file); err != nil {
		s.logger.Error("failed-to-stream-resource-cache", err, lager.Data{"key": key, "path": path})
		panic(http.ErrAbortHandler)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
