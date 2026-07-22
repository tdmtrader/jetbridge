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
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/snapshot"
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

	// Injected only by package-internal durability tests. Production always
	// uses syncRootDirectory so a successful convergence response means the
	// namespace mutation (or observed steady state) was re-synchronized.
	syncSnapshotDirectory func(*os.Root, string) error
}

const snapshotKeyPrefix = "snapshots/sha256/"

var defaultSnapshotMaxBytes = func() int64 {
	limit, err := snapshot.CanonicalArchiveByteLimit(snapshot.DefaultMaxSnapshotContentBytes, snapshot.DefaultMaxSnapshotEntries)
	if err != nil {
		panic(err)
	}
	return limit
}()

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
		syncSnapshotDirectory: syncRootDirectory,
	}
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
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
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

// Handler returns the HTTP handler for the server. When tlsEnabled is true,
// protected routes are wrapped with requireClientCert middleware that returns
// 401 if the request lacks a verified client certificate. Exempt routes
// (/healthz, /resolve, /resolve-batch) are accessible without a client cert.
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

	// Exempt paths — no client cert required (kubelet probes and Prometheus
	// scrapers cannot present client certs; protected by NetworkPolicy).
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /resolve", s.handleResolve)
	mux.HandleFunc("POST /resolve-batch", s.handleResolveBatch)
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
	tlsEnabled bool
}

// WithTLS enables mTLS enforcement on protected routes.
func WithTLS() HandlerOption {
	return func(c *handlerConfig) {
		c.tlsEnabled = true
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (s *Server) artifactPath(r *http.Request) (string, error) {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/artifacts/")
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	if key == r.URL.Path || key == "" || escaped == r.URL.EscapedPath() || strings.Contains(escaped, "%") {
		return "", fmt.Errorf("invalid artifact key")
	}
	if strings.HasPrefix(key, "/") || strings.ContainsAny(key, "\\\x00") || path.Clean(key) != key {
		return "", fmt.Errorf("non-canonical artifact key")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("invalid artifact key segment")
		}
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
	if !strings.HasPrefix(key, snapshotKeyPrefix) {
		return "", true, fmt.Errorf("invalid snapshot namespace path")
	}
	name := strings.TrimPrefix(key, snapshotKeyPrefix)
	if len(name) != 68 || !strings.HasSuffix(name, ".tar") {
		return "", true, fmt.Errorf("invalid snapshot key")
	}
	digest := strings.TrimSuffix(name, ".tar")
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != digest {
		return "", true, fmt.Errorf("invalid snapshot digest")
	}
	return digest, true, nil
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
	if err := root.MkdirAll(parent, 0755); err != nil {
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
			status = "identical"
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
				status = "identical"
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
	status = "created"
	w.WriteHeader(http.StatusCreated)
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

func (s *Server) handleGetSnapshot(w http.ResponseWriter, r *http.Request, digest string) {
	start := time.Now()
	status := "error"
	var copied int64
	defer func() { s.metrics.recordSnapshot("get", status, copied, time.Since(start)) }()
	release := s.guard.BeginRead(snapshotKey(digest))
	defer release()
	root, file, info, ok := s.openSnapshotForRead(w, digest)
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

func (s *Server) handleHeadSnapshot(w http.ResponseWriter, _ *http.Request, digest string) {
	start := time.Now()
	status := "error"
	defer func() { s.metrics.recordSnapshot("head", status, 0, time.Since(start)) }()
	release := s.guard.BeginRead(snapshotKey(digest))
	defer release()
	root, file, info, ok := s.openSnapshotForRead(w, digest)
	if !ok {
		return
	}
	defer root.Close()
	defer file.Close()
	setSnapshotHeaders(w, digest, info.Size())
	status = "ok"
	w.WriteHeader(http.StatusOK)
}

func (s *Server) openSnapshotForRead(w http.ResponseWriter, digest string) (*os.Root, *os.File, os.FileInfo, bool) {
	root, err := os.OpenRoot(s.storagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return nil, nil, nil, false
	}
	key := snapshotKey(digest)
	info, err := root.Lstat(key)
	if err != nil {
		root.Close()
		if errors.Is(err, os.ErrNotExist) {
			http.Error(w, "not found", http.StatusNotFound)
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
	return root, file, info, true
}

func setSnapshotHeaders(w http.ResponseWriter, digest string, size int64) {
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("ETag", `"sha256:`+digest+`"`)
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, _ *http.Request, digest string) {
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
	if err := root.MkdirAll(parent, 0755); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	release := s.guard.BeginSweep(key)
	defer release()
	info, err := root.Lstat(key)
	if errors.Is(err, os.ErrNotExist) {
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
	info, err := os.Stat(path)
	if err != nil && os.IsNotExist(err) {
		// Filesystem miss — try registry lookup.
		if regPath, found := s.lookupRegistryAlias(r); found {
			path = regPath
			info, err = os.Stat(path)
		}
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
	if err := s.validateGenericArtifactSource(path); err != nil {
		http.Error(w, "artifact source is outside the generic artifact namespace", http.StatusBadRequest)
		return
	}

	// Hold the read guard while serving so the sweeper cannot delete the
	// directory mid-stream. Released via defer: the tar-abort path panics.
	release := s.guard.BeginRead(s.stepHandle(path))
	defer release()
	s.touchStepDir(path)

	// Directory: tar on-the-fly and stream.
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		if err := s.tarDirectory(w, path); err != nil {
			s.logger.Error("failed-to-tar-artifact", err, lager.Data{"path": path})
			// The 200 header and part of the body are already out; abort
			// the connection so the client sees a hard failure instead of
			// a clean-looking truncated tar.
			panic(http.ErrAbortHandler)
		}
		return
	}

	// File: serve as-is (backward compat for legacy tar files).
	f, err := os.Open(path)
	if err != nil {
		s.logger.Error("failed-to-open-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		s.logger.Error("failed-to-stream-artifact", err, lager.Data{"path": path})
		panic(http.ErrAbortHandler)
	}
}

// tarDirectory writes a tar archive of the directory to w. Any error —
// including a file changing or disappearing mid-walk — is returned so the
// caller can abort the response; a silently truncated tar reads as complete
// on the client side.
func (s *Server) tarDirectory(w io.Writer, dir string) error {
	tw := tar.NewWriter(w)

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		hdr := &tar.Header{
			Name:    rel,
			Size:    info.Size(),
			Mode:    int64(info.Mode()),
			ModTime: info.ModTime(),
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, _ := os.Readlink(path)
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = link
			hdr.Size = 0
			return tw.WriteHeader(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		// Deliberately skip tw.Close(): writing the tar terminator would
		// make the truncated stream parse as a complete archive.
		return err
	}
	return tw.Close()
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
	if err := s.validateGenericArtifactSource(path); err != nil {
		http.Error(w, "artifact destination is outside the generic artifact namespace", http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		s.logger.Error("failed-to-create-artifact-dir", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Write through a temp file + rename: GET serves whatever exists at the
	// final path, so an in-flight or failed upload must never be visible
	// there as a truncated artifact.
	f, err := os.CreateTemp(filepath.Dir(path), ".put-tmp-")
	if err != nil {
		s.logger.Error("failed-to-create-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	tmpPath := f.Name()

	if _, err := io.Copy(f, r.Body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		s.logger.Error("failed-to-write-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		s.logger.Error("failed-to-close-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// CreateTemp uses 0600; artifacts are served to any authenticated reader.
	if err := os.Chmod(tmpPath, 0644); err != nil {
		os.Remove(tmpPath)
		s.logger.Error("failed-to-chmod-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		s.logger.Error("failed-to-rename-artifact", err, lager.Data{"path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

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
	if err := os.MkdirAll(stepsRoot, 0755); err != nil {
		s.logger.Error("failed-to-create-stream-in-dir", err, lager.Data{"key": key})
		http.Error(w, "create dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := verifyExistingPathComponents(s.storagePath, "steps", false); err != nil {
		http.Error(w, "invalid steps root", http.StatusInternalServerError)
		return
	}
	if err := verifyExistingPathComponents(stepsRoot, filepath.FromSlash(key), false); err != nil {
		http.Error(w, "unsafe stream-in destination", http.StatusBadRequest)
		return
	}
	stepsHandle, err := os.OpenRoot(stepsRoot)
	if err != nil {
		s.logger.Error("failed-to-anchor-stream-in-dir", err, lager.Data{"key": key})
		http.Error(w, "anchor steps dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer stepsHandle.Close()
	tmpName, err := createRootTempDir(stepsHandle, ".in-tmp-")
	if err != nil {
		s.logger.Error("failed-to-create-stream-in-tmp-dir", err, lager.Data{"key": key})
		http.Error(w, "create tmp dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	tmpDest := filepath.Join(stepsRoot, tmpName)
	// No-op after the successful rename; cleans up on every error path.
	defer stepsHandle.RemoveAll(tmpName)

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

	extractionRoot, err := stepsHandle.OpenRoot(tmpName)
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
	destRel := filepath.FromSlash(key)
	if err := stepsHandle.MkdirAll(filepath.Dir(destRel), 0755); err != nil {
		s.logger.Error("failed-to-create-stream-in-parent", err, lager.Data{"key": key})
		http.Error(w, "create parent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	renameErr := func() error {
		release := s.guard.BeginSweep(s.stepHandle(dest))
		defer release()
		if err := stepsHandle.RemoveAll(destRel); err != nil {
			return fmt.Errorf("remove stale stream-in destination: %w", err)
		}
		return stepsHandle.Rename(tmpName, destRel)
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
	if err := s.validateGenericArtifactSource(path); err != nil {
		http.Error(w, "artifact target is outside the generic artifact namespace", http.StatusBadRequest)
		return
	}

	// Deletion is destructive like a sweep: wait out in-flight reads so a
	// concurrent copy never sees a half-removed tree.
	release := s.guard.BeginSweep(s.stepHandle(path))
	defer release()

	err = os.RemoveAll(path)
	if err != nil {
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

	// Check filesystem first, then fall back to registry aliases.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			if regPath, found := s.lookupRegistryAlias(r); found {
				if _, err := os.Stat(regPath); err == nil {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"path": path})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if err := s.validateGenericArtifactSource(path); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// lookupRegistryAlias checks the registry for an artifact key extracted from
// the request URL. Peer probes send URLs like /artifacts/steps/rc-42, yielding
// the key "steps/rc-42" — but the registry stores just "rc-42". We try the
// full key first, then strip common prefixes.
func (s *Server) lookupRegistryAlias(r *http.Request) (string, bool) {
	key := strings.TrimPrefix(r.URL.Path, "/artifacts/")
	if artifactPath, found := s.registry.Lookup(key); found {
		if _, err := s.validateStepPath(artifactPath, true); err == nil {
			return artifactPath, true
		}
		s.registry.Remove(key)
	}
	// Strip "steps/" prefix — peer probes prepend it but aliases don't have it.
	if stripped := strings.TrimPrefix(key, "steps/"); stripped != key {
		if artifactPath, found := s.registry.Lookup(stripped); found {
			if _, err := s.validateStepPath(artifactPath, true); err == nil {
				return artifactPath, true
			}
			s.registry.Remove(stripped)
		}
	}
	return "", false
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
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
	Key  string `json:"key"`
	Dest string `json:"dest"`
}

// resolveResponse is the JSON body returned by POST /resolve.
type resolveResponse struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Method   string `json:"method"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

// handleRegister accepts POST /register with a JSON body containing
// {key, local_path} and registers the artifact in the daemon's registry.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
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

	// Validate that the path exists on disk.
	if _, err := os.Stat(req.LocalPath); err != nil {
		if os.IsNotExist(err) {
			s.logger.Info("register-path-not-found", lager.Data{"key": req.Key, "path": req.LocalPath})
			http.Error(w, fmt.Sprintf("path not found: %s", req.LocalPath), http.StatusNotFound)
			return
		}
		s.logger.Error("register-stat-error", err, lager.Data{"key": req.Key, "path": req.LocalPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if _, err := s.validateStepPath(req.LocalPath, true); err != nil {
		http.Error(w, "registration source must be a real path beneath the steps root", http.StatusBadRequest)
		return
	}

	s.registry.RegisterAlias(req.Key, req.LocalPath)

	s.logger.Info("registered", lager.Data{"key": req.Key, "path": req.LocalPath})
	w.WriteHeader(http.StatusCreated)
}

// resolveOne resolves a single artifact key to a destination path.
// It is the core logic shared by handleResolve and handleResolveBatch.
func (s *Server) resolveOne(ctx context.Context, key, dest string) (resp resolveResponse) {
	start := time.Now()
	defer func() {
		s.metrics.recordResolve(resp.Method, resp.Status, time.Since(start))
	}()
	logger := s.logger.Session("resolve", lager.Data{"key": key, "dest": dest})
	if err := s.validateResolveBoundary(key, dest); err != nil {
		return resolveResponse{Status: "error", Method: "validation", Error: err.Error()}
	}

	// Step 1: Check registry for explicit registration.
	sourcePath, found := s.registry.Lookup(key)
	if found {
		if _, err := s.validateStepPath(sourcePath, true); err != nil {
			s.registry.Remove(key)
			return resolveResponse{Status: "error", Source: sourcePath, Method: "validation", Error: fmt.Sprintf("unsafe registry source: %v", err)}
		}
		if err := s.copyArtifactGuarded(sourcePath, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": sourcePath})
			return resolveResponse{Status: "error", Source: sourcePath, Method: "local", Error: err.Error()}
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "registry", "source": sourcePath, "duration": duration.String()})
		return resolveResponse{Status: "ok", Source: sourcePath, Method: "registry", Duration: duration.String()}
	}

	// Step 2: Fallback — check if key maps to a steps/ directory on disk.
	stepsPath := filepath.Join(s.storagePath, "steps", key)
	if info, err := os.Stat(stepsPath); err == nil && info.IsDir() {
		if _, err := s.validateStepPath(stepsPath, true); err != nil {
			return resolveResponse{Status: "error", Source: stepsPath, Method: "validation", Error: fmt.Sprintf("unsafe filesystem source: %v", err)}
		}
		s.registry.Register(key, stepsPath)

		if err := s.copyArtifactGuarded(stepsPath, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": stepsPath})
			return resolveResponse{Status: "error", Source: stepsPath, Method: "filesystem", Error: err.Error()}
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "filesystem", "source": stepsPath, "duration": duration.String()})
		return resolveResponse{Status: "ok", Source: stepsPath, Method: "filesystem", Duration: duration.String()}
	}

	// Step 3: Query peer daemons for cross-node resolution.
	if s.peers != nil {
		peerIP, found := s.peers.Probe(ctx, key)
		if found {
			if err := s.fetchPeerArtifact(ctx, peerIP, key, dest); err != nil {
				logger.Error("peer-fetch-failed", err, lager.Data{"peer": peerIP})
				return resolveResponse{Status: "error", Source: peerIP, Method: "peer", Error: err.Error()}
			}
			duration := time.Since(start)
			logger.Info("resolved", lager.Data{"method": "peer", "peer": peerIP, "duration": duration.String()})
			return resolveResponse{Status: "ok", Source: peerIP, Method: "peer", Duration: duration.String()}
		}
	}

	// Step 4: Not found anywhere.
	duration := time.Since(start)
	logger.Info("not-found", lager.Data{"duration": duration.String()})
	return resolveResponse{Status: "not_found", Method: "exhausted", Duration: duration.String(), Error: fmt.Sprintf("artifact %q not found on this node or any peer", key)}
}

func (s *Server) fetchPeerArtifact(ctx context.Context, peerIP, key, dest string) error {
	stepsRoot := filepath.Join(s.storagePath, "steps")
	destRel, err := pathBelow(stepsRoot, dest)
	if err != nil {
		return fmt.Errorf("derive peer destination: %w", err)
	}
	if err := os.MkdirAll(stepsRoot, 0755); err != nil {
		return fmt.Errorf("create peer steps root: %w", err)
	}
	if err := verifyExistingPathComponents(s.storagePath, "steps", false); err != nil {
		return fmt.Errorf("validate peer steps root: %w", err)
	}
	stepsHandle, err := os.OpenRoot(stepsRoot)
	if err != nil {
		return fmt.Errorf("anchor peer destination: %w", err)
	}
	defer stepsHandle.Close()
	if err := stepsHandle.MkdirAll(filepath.Dir(destRel), 0755); err != nil {
		return fmt.Errorf("create peer destination parent: %w", err)
	}
	tmpName, err := createRootTempDir(stepsHandle, ".peer-stage-")
	if err != nil {
		return fmt.Errorf("create peer staging directory: %w", err)
	}
	defer stepsHandle.RemoveAll(tmpName)
	if err := s.peers.Fetch(ctx, peerIP, key, filepath.Join(stepsRoot, tmpName)); err != nil {
		return err
	}

	release := s.guard.BeginSweep(s.stepHandle(dest))
	defer release()
	if err := stepsHandle.RemoveAll(destRel); err != nil {
		return fmt.Errorf("remove stale peer destination: %w", err)
	}
	if err := stepsHandle.Rename(tmpName, destRel); err != nil {
		return fmt.Errorf("publish peer artifact: %w", err)
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
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
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

	resp := s.resolveOne(r.Context(), req.Key, req.Dest)

	status := http.StatusOK
	if resp.Status == "error" {
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
func (s *Server) handleResolveBatch(w http.ResponseWriter, r *http.Request) {
	var req batchResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	destinations := make([]string, 0, len(req.Items))
	for _, item := range req.Items {
		if item.Key == "" || item.Dest == "" {
			http.Error(w, "every item requires key and dest", http.StatusBadRequest)
			return
		}
		if err := s.validateResolveBoundary(item.Key, item.Dest); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, existing := range destinations {
			if pathsOverlap(existing, item.Dest) {
				http.Error(w, "batch destinations must not overlap", http.StatusBadRequest)
				return
			}
		}
		destinations = append(destinations, item.Dest)
	}

	results := make([]resolveResponse, len(req.Items))

	var wg sync.WaitGroup
	for i, item := range req.Items {
		wg.Add(1)
		go func(idx int, key, dest string) {
			defer wg.Done()
			results[idx] = s.resolveOne(r.Context(), key, dest)
		}(i, item.Key, item.Dest)
	}
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
	}

	writeJSON(w, status, batchResolveResponse{Status: overall, Results: results})
}

// copyArtifactGuarded wraps copyArtifact with the read guard and a
// pre-copy mtime touch: the guard keeps the sweeper from deleting src
// mid-copy (cp -R silently omits files removed before enumeration), and the
// touch makes the sweeper's under-lock re-check spare the directory.
func (s *Server) copyArtifactGuarded(src, dest string) error {
	release := s.guard.BeginRead(s.stepHandle(src))
	defer release()
	s.touchStepDir(src)
	return s.copyArtifact(src, dest)
}

// copyArtifact copies the contents of src directory to dest atomically.
// It copies into a temporary sibling directory first, then renames to the
// final path. This prevents partial state from blocking retries when a
// previous copy was interrupted (e.g., by restrictive or read-only files
// left in the destination).
func (s *Server) copyArtifact(src, dest string) error {
	if _, err := s.validateStepPath(src, true); err != nil {
		return fmt.Errorf("validate source boundary: %w", err)
	}
	if _, err := s.validateStepPath(dest, false); err != nil {
		return fmt.Errorf("validate destination boundary: %w", err)
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
	stepsHandle, err := os.OpenRoot(stepsRoot)
	if err != nil {
		return fmt.Errorf("anchor steps root: %w", err)
	}
	defer stepsHandle.Close()
	if err := stepsHandle.MkdirAll(filepath.Dir(destRel), 0755); err != nil {
		return fmt.Errorf("create destination parent: %w", err)
	}
	// Keep the temporary copy at the anchored steps root. Publication and
	// cleanup then remain confined even if a destination parent is replaced by
	// a symlink between validation and rename.
	tmpName, err := createRootTempDir(stepsHandle, ".cp-tmp-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	tmpDest := filepath.Join(stepsRoot, tmpName)
	defer stepsHandle.RemoveAll(tmpName)

	// Use cp -R (recursive only — no ownership/mode preservation). The daemon
	// has CAP_DAC_OVERRIDE to read source files owned by any UID, but does NOT
	// have CAP_CHOWN. GNU cp -p as root treats chown failure as a hard error,
	// so we must not use -p. Ownership/mode preservation is unnecessary anyway —
	// these are ephemeral artifact cache copies.
	cmd := exec.Command("cp", "-R", src+"/.", tmpDest+"/")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp -R %s/. %s/: %w (output: %s)", src, tmpDest, err, strings.TrimSpace(string(output)))
	}

	// Ensure world-readable permissions so non-root task containers can access
	// artifacts. Source files may have restrictive modes (e.g. 0600) set by the
	// producing step's UID. The daemon owns the copies (root:root) so chmod
	// succeeds without CAP_FOWNER. "a+rX" adds read for all and execute only
	// on directories (where owner already has execute).
	chmodCmd := exec.Command("chmod", "-R", "a+rX", tmpDest)
	if output, err := chmodCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("chmod -R a+rX %s: %w (output: %s)", tmpDest, err, strings.TrimSpace(string(output)))
	}

	// Remove and publish through the anchored root; os.Root refuses to follow
	// a path component outside the steps namespace.
	if err := stepsHandle.RemoveAll(destRel); err != nil {
		return fmt.Errorf("remove stale destination: %w", err)
	}
	if err := stepsHandle.Rename(tmpName, destRel); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpDest, dest, err)
	}
	return nil
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

	path, found := s.registry.Lookup(key)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Verify the path still exists on disk — aliases can become stale if
	// the sweeper removed the step directory.
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			s.registry.Remove(key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "path": path})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if _, err := s.validateStepPath(path, true); err != nil {
		s.registry.Remove(key)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

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

	path, found := s.registry.Lookup(key)
	if !found {
		http.NotFound(w, r)
		return
	}
	if _, err := s.validateStepPath(path, true); err != nil {
		s.registry.Remove(key)
		http.Error(w, "invalid resource cache source", http.StatusBadRequest)
		return
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.registry.Remove(key)
			http.NotFound(w, r)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}

	// Same guard discipline as handleGetArtifact: no sweeps mid-stream.
	release := s.guard.BeginRead(s.stepHandle(path))
	defer release()
	s.touchStepDir(path)

	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		if err := s.tarDirectory(w, path); err != nil {
			s.logger.Error("failed-to-tar-resource-cache", err, lager.Data{"key": key, "path": path})
			// Headers already sent; abort so the peer sees a hard failure
			// rather than a clean-looking truncated tar.
			panic(http.ErrAbortHandler)
		}
		return
	}

	f, err := os.Open(path)
	if err != nil {
		s.logger.Error("resource-cache-open-error", err, lager.Data{"key": key, "path": path})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		s.logger.Error("failed-to-stream-resource-cache", err, lager.Data{"key": key, "path": path})
		panic(http.ErrAbortHandler)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
