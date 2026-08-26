package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"golang.org/x/sync/singleflight"

	"github.com/concourse/concourse/artifactcap"
	"github.com/concourse/concourse/cmd/artifact-daemon/durable"
)

// Server is the artifact-daemon HTTP server that stores and serves
// artifact tar files from local hostPath storage.
type Server struct {
	logger        lager.Logger
	storagePath   string
	nodeName      string
	registry      *Registry
	peers         *PeerResolver
	mirrorTrigger func(ctx context.Context, key string)
	metrics       *metrics
	guard         *ReadGuard
	durable       *DurableTier

	// restoreFlight collapses concurrent durable restores of one key.
	restoreFlight singleflight.Group
	// uploadSem bounds concurrent durable uploads. Each spools a whole
	// artifact through temporary storage, so unbounded promotion is a way to
	// run the node out of disk.
	uploadSem chan struct{}
	// resolveSem bounds concurrent batch resolves. Same reasoning as uploadSem,
	// on the endpoint that needs it more: /resolve-batch is mTLS-exempt, and it
	// spawned one goroutine per request item with no cap, each running cp -R
	// and chmod -R. The authenticated path was bounded and the unauthenticated
	// one was not.
	resolveSem chan struct{}
	// destLocks serialises copies by DESTINATION. copyArtifactGuarded locks on
	// the SOURCE handle, so two items with different keys and the same dest
	// raced on os.RemoveAll(dest) and os.Rename — reachable with entirely
	// legitimate keys.
	destLocks sync.Map

	// root is the handle every filesystem operation on artifact data goes
	// through. Containment is a property of the handle, not of a check
	// performed before the operation: os.Root refuses to resolve a path out of
	// itself, so there is no ordering to get wrong and no name to out-think.
	root *os.Root

	// resolveVerifier authenticates the two mTLS-exempt resolve routes. nil
	// means no key was configured and they are unauthenticated.
	resolveVerifier *artifactcap.Verifier
}

// destLock serialises copies to one destination and refcounts its own lifetime,
// so the map holding it is bounded by IN-FLIGHT copies rather than by every
// destination ever seen.
type destLock struct {
	mu      sync.Mutex
	waiters atomic.Int64
}

// maxConcurrentDurableUploads caps in-flight promotions per daemon.
const maxConcurrentDurableUploads = 4

// maxConcurrentBatchResolves caps in-flight copies from one batch request.
// Production batches are one item per step input — single digits — against a
// 180s per-attempt timeout, so this is invisible to legitimate traffic.
const maxConcurrentBatchResolves = 4

// maxJSONBodyBytes caps the JSON control-plane bodies. Deliberately NOT applied
// to PUT /stream-in/ or PUT /artifacts/, which stream whole artifacts: every
// mirror push and ATC upload goes through those, and a cap there would break
// artifact delivery outright.
const maxJSONBodyBytes = 1 << 20

// NewServer creates a new artifact-daemon server.
// NewServer can now fail: it acquires the storage root at construction.
//
// Deliberately at construction rather than lazily. A lazy open converts a boot
// failure into a per-request failure and makes "every operation goes through
// the handle" conditional on something having succeeded earlier; opening it in
// Handler() cannot work because Mirror, Sweeper and DurableTier are built
// outside it and need the same root.
func NewServer(logger lager.Logger, storagePath, nodeName string) (*Server, error) {
	root, err := os.OpenRoot(storagePath)
	if err != nil {
		return nil, fmt.Errorf("open storage root %q: %w", storagePath, err)
	}

	return &Server{
		logger:      logger,
		storagePath: storagePath,
		nodeName:    nodeName,
		registry:    NewRegistry(logger, storagePath),
		metrics:     newMetrics(),
		guard:       NewReadGuard(),
		uploadSem:   make(chan struct{}, maxConcurrentDurableUploads),
		resolveSem:  make(chan struct{}, maxConcurrentBatchResolves),
		root:        root,
	}, nil
}

// SetDurableTier attaches the long-term store. When unset the daemon behaves
// exactly as it did before: a resource cache that is not on this node or a peer
// is simply a miss.
func (s *Server) SetDurableTier(tier *DurableTier) {
	s.durable = tier
}

// Guard returns the read/sweep coordination guard. The sweeper takes its
// exclusive side per directory removal so reads never copy from a directory
// being deleted.
func (s *Server) Guard() *ReadGuard {
	return s.guard
}

// stepHandle returns the guard key for a location: the {handle} segment for
// anything under steps/, or the location itself for non-steps entries (legacy
// flat files) so they still get a consistent per-location lock.
//
// The key MUST be identical to the sweeper's, which is entry.Name() — a bare
// handle — or the two stop excluding each other and the sweeper deletes a
// directory mid-read. That failure is silent and loses data rather than
// hanging, which is why the argument is a RelKey and not a path.
func (s *Server) stepHandle(rel RelKey) string {
	segments := strings.Split(string(rel), "/")
	if len(segments) >= 2 && segments[0] == "steps" {
		return segments[1]
	}
	// Namespaced so a non-steps location cannot collide with a bare step
	// handle: without it, "build-42" and "steps/build-42/out" both key on
	// "build-42" and unrelated work serialises. The sweeper only ever keys on a
	// bare handle, so the prefix can never collide with it.
	return "loc:" + string(rel)
}

// MirrorOriginHeader marks a PUT /stream-in as originating from a peer
// daemon's mirror. Such writes must not re-trigger mirroring: the origin
// daemon fans out to all chosen peers itself, and a re-trigger makes peers
// ping-pong the same key forever, each racy hop able to propagate a
// truncated copy.
const MirrorOriginHeader = "X-Concourse-Mirror"

// Registry returns the server's artifact registry.
// Metrics returns the server's collector set, so components constructed
// alongside the Server can record into the same registry.
func (s *Server) Metrics() *metrics {
	return s.metrics
}

func (s *Server) Registry() *Registry {
	return s.registry
}

// SetResolveCapabilityKey requires every resolve to carry a capability bound to
// its exact key and destination.
//
// /resolve and /resolve-batch are mTLS-EXEMPT by design: the init container
// dials the daemon by node IP, which cannot be a certificate SAN. This is
// therefore the only authentication those two routes can have — and they take a
// caller-supplied dest that becomes a RemoveAll and a Rename.
//
// When no key is configured the routes stay open, because that is how every
// deployment without the flag behaves today. main.go logs loudly in that case;
// the chart always sets it.
func (s *Server) SetResolveCapabilityKey(key []byte) error {
	v, err := artifactcap.NewVerifier(key)
	if err != nil {
		return err
	}
	s.resolveVerifier = v
	return nil
}

// authorizeResolve fails CLOSED: once a key is configured, a missing or
// unverifiable capability is refused rather than warned about.
func (s *Server) authorizeResolve(req resolveRequest) bool {
	if s.resolveVerifier == nil {
		return true
	}
	if req.Capability == "" {
		return false
	}
	return s.resolveVerifier.VerifyResolve(req.Capability, req.Key, req.Dest, time.Now()) == nil
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
	mux.HandleFunc("POST /durable/restore", protect(s.handleDurableRestore))
	mux.HandleFunc("HEAD /resource-caches/", protect(s.handleHeadResourceCache))
	mux.HandleFunc("GET /resource-caches/", protect(s.handleGetResourceCache))

	return mux
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

// requestKey is the ONLY place a request URL becomes a key. Every handler that
// needs one goes through here, so there is exactly one place the containment
// rule can be forgotten.
//
// It returns the key rather than a path because three of its five callers want
// the key: lookupRegistryAlias strips a "steps/" prefix and does a second
// registry lookup, and both /resource-caches/ handlers look the key up in the
// registry without joining anything. An accessor that returned a joined path
// would force them to un-join it.
func (s *Server) requestKey(r *http.Request, prefix string) (string, error) {
	key := strings.TrimPrefix(r.URL.Path, prefix)
	if err := validateRequestKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// artifactKey returns the validated key an /artifacts/ request names.
//
// Operations go through s.root with this key. The absolute form is still
// needed by the read/sweep guard (whose keys stay absolute under R12) and by
// tarDirectory (which walks a path; egress belongs to another track), and
// artifactPath above produces it — routed through artifactLocation so the
// walked path keeps the containment check the walker itself does not have.
func (s *Server) artifactKey(r *http.Request) (string, error) {
	key, err := s.requestKey(r, "/artifacts/")
	if err != nil {
		return "", err
	}
	if _, err := s.artifactLocation(s.storagePath, key); err != nil {
		return "", err
	}
	return key, nil
}

func (s *Server) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	key, err := s.artifactKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The request key IS a location under the root: artifactKey has already run
	// it through artifactLocation. This is the boundary where a validated
	// request key becomes a RelKey, and the only reason the conversion is
	// sound.
	loc := RelKey(key)

	// Check filesystem first, then fall back to registry aliases.
	// This enables peer daemons to serve registry-only artifacts
	// (e.g., resource caches registered via POST /register).
	//
	// The fallback used to substitute an ABSOLUTE alias path, which forced this
	// handler to carry a servedFromRegistry flag and branch between os.Open and
	// root.Open — and migrating one branch and not the other is how this broke
	// once already. Registry values are now locations under the same root, so
	// there is one representation and one code path.
	info, err := s.root.Stat(osName(loc))
	if err != nil && os.IsNotExist(err) {
		if regLoc, found := s.lookupRegistryAlias(r); found {
			loc = regLoc
			info, err = s.root.Stat(osName(loc))
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Hold the read guard while serving so the sweeper cannot delete the
	// directory mid-stream. Released via defer: the tar-abort path panics.
	release := s.guard.BeginRead(s.stepHandle(loc))
	defer release()
	s.touchStepDir(loc)

	// Directory: tar on-the-fly and stream.
	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		if err := s.tarDirectory(w, loc); err != nil {
			s.logger.Error("failed-to-tar-artifact", err, lager.Data{"rel": string(loc)})
			// The 200 header and part of the body are already out; abort
			// the connection so the client sees a hard failure instead of
			// a clean-looking truncated tar.
			panic(http.ErrAbortHandler)
		}
		return
	}

	// File: serve as-is (backward compat for legacy tar files).
	//
	// Open by KEY through the handle normally. When the registry fallback
	// substituted an alias path, open THAT instead — the key is the one that
	// did not exist, which is why we fell back, and opening it returns a 500 on
	// a request that should succeed. The directory branch above already uses
	// `path` for exactly this reason; migrating one branch and not the other is
	// how this broke.
	//
	f, err := s.root.Open(osName(loc))
	if err != nil {
		s.logger.Error("failed-to-open-artifact", err, lager.Data{"rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		s.logger.Error("failed-to-stream-artifact", err, lager.Data{"rel": string(loc)})
		panic(http.ErrAbortHandler)
	}
}

// tarDirectory writes a tar archive of the directory to w. Any error —
// including a file changing or disappearing mid-walk — is returned so the
// caller can abort the response; a silently truncated tar reads as complete
// on the client side.
func (s *Server) tarDirectory(w io.Writer, loc RelKey) error {
	return tarTree(w, s.root, loc)
}

// touchStepDir bumps the mtime of the steps/{handle} directory containing rel
// (when rel is under steps/) so the TTL sweeper treats actively-read artifacts
// as fresh. Best-effort, and through the root handle.
func (s *Server) touchStepDir(rel RelKey) {
	segments := strings.Split(string(rel), "/")
	if len(segments) < 2 || segments[0] != "steps" {
		return
	}
	now := time.Now()
	_ = s.root.Chtimes(path.Join("steps", segments[1]), now, now)
}

func (s *Server) handlePutArtifact(w http.ResponseWriter, r *http.Request) {
	key, err := s.artifactKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(s.storagePath, key)

	if dir := path.Dir(key); dir != "." {
		if err := s.root.MkdirAll(dir, 0755); err != nil {
			s.logger.Error("failed-to-create-artifact-dir", err, lager.Data{"path": absPath})
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}

	// Write through a temp file + rename: GET serves whatever exists at the
	// final path, so an in-flight or failed upload must never be visible
	// there as a truncated artifact.
	// os.Root has no CreateTemp, so the temp name is made in the same
	// hand-rolled way as mkdirTempIn and opened through the handle.
	tmpKey := path.Join(path.Dir(key), ".put-tmp-"+strconv.FormatUint(rand.Uint64(), 36))
	f, err := s.root.OpenFile(tmpKey, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		s.logger.Error("failed-to-create-artifact", err, lager.Data{"path": absPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if _, err := io.Copy(f, r.Body); err != nil {
		f.Close()
		s.root.Remove(tmpKey)
		s.logger.Error("failed-to-write-artifact", err, lager.Data{"path": absPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		s.root.Remove(tmpKey)
		s.logger.Error("failed-to-close-artifact", err, lager.Data{"path": absPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// CreateTemp uses 0600; artifacts are served to any authenticated reader.
	if err := s.root.Chmod(tmpKey, 0644); err != nil {
		s.root.Remove(tmpKey)
		s.logger.Error("failed-to-chmod-artifact", err, lager.Data{"path": absPath})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if err := s.root.Rename(tmpKey, key); err != nil {
		s.root.Remove(tmpKey)
		s.logger.Error("failed-to-rename-artifact", err, lager.Data{"path": absPath})
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
	key, err := s.requestKey(r, "/stream-in/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Structural-name AUTHORIZATION survives the handle: os.Root has no opinion
	// about WHICH names a per-artifact verb may address and will happily create
	// steps/aliases.json. Containment and authorization were once fused here,
	// and replacing only the containment half let PUT /stream-in/aliases.json
	// return 201.
	if err := rejectStructuralName(key); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// No containment check here: it is a property of stepsRoot below, where
	// every operation resolves inside the handle or fails.
	//
	// The stream-in key is relative to the STEPS root while the registry and
	// the guard are relative to the storage root, so the location carries the
	// "steps/" segment. Both are RelKey, so getting it wrong is not a compile
	// error — stated once here and derived everywhere else.
	loc := RelKey(path.Join("steps", key))
	dest := filepath.Join(s.storagePath, "steps", key)

	// The steps/ boundary is a HANDLE, not an argument.
	//
	// An earlier track passed the boundary as a parameter and then validated
	// against a different one, so a symlink under steps/ pointing at the store
	// root satisfied "contained" while plainly escaping steps/. A nested root
	// cannot be passed and ignored: every operation below resolves inside it or
	// fails.
	if err := s.root.MkdirAll("steps", 0755); err != nil {
		s.logger.Error("failed-to-create-stream-in-dir", err, lager.Data{"key": key})
		http.Error(w, "create dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	stepsRoot, err := s.root.OpenRoot("steps")
	if err != nil {
		s.logger.Error("failed-to-open-steps-root", err, lager.Data{"key": key})
		http.Error(w, "open steps root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer stepsRoot.Close()

	// Extract into a temp sibling and rename into place on success. Readers
	// (resolve, GET/HEAD, peer probes) treat any existing steps/{key} dir as
	// a complete artifact, so partial state — an in-flight extraction or a
	// failed upload — must never be visible at the final path.
	tmpName, err := mkdirTempIn(stepsRoot, ".in-tmp-")
	if err != nil {
		s.logger.Error("failed-to-create-stream-in-tmp-dir", err, lager.Data{"key": key})
		http.Error(w, "create tmp dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// No-op after the successful rename; cleans up on every error path.
	defer stepsRoot.RemoveAll(tmpName)

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

	// One extraction implementation, not two. This handler carried its own
	// tar loop with a strings.HasPrefix boundary and an unvalidated
	// os.Symlink; extractTarToDir has driven every entry through an os.Root
	// handle since Track 1 and has been adversarially reviewed five times.
	//
	// What stays here: the gzip peek above (extractTarToDir takes a plain
	// reader), the temp-dir/rename machinery below, and the registry
	// registration. Only the loop goes.
	tmpRoot, err := stepsRoot.OpenRoot(tmpName)
	if err != nil {
		s.logger.Error("failed-to-open-tmp-root", err, lager.Data{"key": key})
		http.Error(w, "open tmp root: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tmpRoot.Close()

	if err := extractTarToRoot(tarSource, tmpRoot); err != nil {
		// Attribution, not containment. mirror.go reads any non-201 from a
		// peer as "rejected", so an archive-attributable failure reported as
		// 500 makes a poisoned artifact look like a peer fault.
		status := http.StatusInternalServerError
		if errors.Is(err, ErrRefused) {
			status = http.StatusBadRequest
		}
		s.logger.Error("failed-to-extract-stream-in", err, lager.Data{"key": key, "status": status})
		http.Error(w, err.Error(), status)
		return
	}

	// Move the fully-extracted tree into place. Remove any previous copy
	// first (rename onto a non-empty dir fails). The replace is destructive
	// like a sweep: take the handle's exclusive lock so in-flight reads
	// (resolve copies, GET/mirror tar walks) never see a half-removed tree.
	if dir := path.Dir(key); dir != "." {
		if err := stepsRoot.MkdirAll(dir, 0755); err != nil {
			s.logger.Error("failed-to-create-stream-in-parent", err, lager.Data{"key": key})
			http.Error(w, "create parent: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	renameErr := func() error {
		// stepHandle(loc) is the same key the sweeper derives from the handle
		// directory's name. That equality is what makes this lock exclude the
		// sweeper at all; when the two representations disagreed it did not.
		release := s.guard.BeginSweep(s.stepHandle(loc))
		defer release()
		stepsRoot.RemoveAll(key)
		return stepsRoot.Rename(tmpName, key)
	}()
	if renameErr != nil {
		s.logger.Error("failed-to-rename-stream-in", renameErr, lager.Data{"key": key, "tmp": tmpName})
		http.Error(w, "rename: "+renameErr.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := s.registry.Register(key, dest); err != nil {
		// The tree is already in place under the root handle, so this cannot
		// mean an escape — only that storagePath itself stopped resolving.
		// Serving it would mean reporting 201 for an artifact no lookup can
		// find, which presents later as an unexplained cache miss.
		s.logger.Error("failed-to-register-stream-in", err, lager.Data{"key": key, "dest": dest})
		http.Error(w, "register: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.logger.Info("stream-in-complete", lager.Data{"key": key, "rel": string(loc)})

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
	key, err := s.artifactKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	loc := RelKey(key) // validated by artifactKey

	// Deletion is destructive like a sweep: wait out in-flight reads so a
	// concurrent copy never sees a half-removed tree.
	release := s.guard.BeginSweep(s.stepHandle(loc))
	defer release()

	if err := s.root.RemoveAll(osName(loc)); err != nil {
		s.logger.Error("failed-to-delete-artifact", err, lager.Data{"rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleHeadArtifact(w http.ResponseWriter, r *http.Request) {
	key, err := s.artifactKey(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	loc := RelKey(key) // canonical: validateRequestKey refuses anything else

	// Check filesystem first, then fall back to registry aliases. Both stats go
	// through the root handle now that the registry value is a location under
	// the same root.
	if _, err := s.root.Stat(osName(loc)); err != nil {
		if os.IsNotExist(err) {
			if regLoc, found := s.lookupRegistryAlias(r); found {
				if _, err := s.root.Stat(osName(regLoc)); err == nil {
					w.WriteHeader(http.StatusOK)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("failed-to-stat-artifact", err, lager.Data{"key": key})
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// lookupRegistryAlias checks the registry for an artifact key extracted from
// the request URL. Peer probes send URLs like /artifacts/steps/rc-42, yielding
// the key "steps/rc-42" — but the registry stores just "rc-42". We try the
// full key first, then strip common prefixes.
func (s *Server) lookupRegistryAlias(r *http.Request) (RelKey, bool) {
	key, err := s.requestKey(r, "/artifacts/")
	if err != nil {
		return "", false
	}
	if rel, found := s.lookupRegistry(key); found {
		return rel, true
	}
	// Strip "steps/" prefix — peer probes prepend it but aliases don't have it.
	if stripped := strings.TrimPrefix(key, "steps/"); stripped != key {
		if rel, found := s.lookupRegistry(stripped); found {
			return rel, true
		}
	}
	return "", false
}

// registerRequest is the JSON body for POST /register.
type registerRequest struct {
	Key       string `json:"key"`
	LocalPath string `json:"local_path"`

	// DurableKey is the name to store this artifact under for the long term,
	// or empty for "do not keep it".
	//
	// Its presence is the entire eligibility protocol: whether an artifact is
	// re-derivable and how long to keep it are questions only the ATC can
	// answer, so the daemon neither parses this nor derives it from Key.
	//
	// Deliberately a separate field: Key names a node-local alias and stays a
	// single segment; DurableKey names a bucket object and carries a
	// retention-class prefix that a lifecycle rule acts on.
	DurableKey string `json:"durable_key,omitempty"`
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
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req mirrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}
	// Mirror.run joins this onto the storage root and tars the result to every
	// peer, so an unvalidated key reads a tree outside the root and ships it
	// off-node.
	if err := validateRequestKey(req.Key); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	// Capability is a short-lived token bound to this exact Key and Dest,
	// signed by the ATC with a key both sides share. Required whenever the
	// daemon was started with --resolve-capability-key.
	Capability string `json:"capability,omitempty"`
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
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.Key == "" || req.LocalPath == "" {
		http.Error(w, "key and local_path are required", http.StatusBadRequest)
		return
	}

	// Validate EVERYTHING before the first side effect. This handler used to
	// call RegisterAlias and persist it to disk before validating DurableKey,
	// so a 400 left a poisoned alias behind that survived a restart.
	if err := validateRequestKey(req.Key); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateContainedPath(s.storagePath, req.LocalPath); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.DurableKey != "" {
		if err := durable.ValidateKey(req.DurableKey); err != nil {
			http.Error(w, fmt.Sprintf("invalid durable_key: %v", err), http.StatusBadRequest)
			return
		}
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

	// REFUSE here rather than store-and-hope. req.LocalPath is client-supplied,
	// so this is the one Register call whose input is not derived from the
	// daemon's own tree — and 400 is the honest status: the caller named a path
	// outside the store.
	loc, err := s.registry.RegisterAlias(req.Key, req.LocalPath)
	if err != nil {
		s.logger.Info("register-refused", lager.Data{"key": req.Key, "path": req.LocalPath, "reason": err.Error()})
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if req.DurableKey != "" {
		s.promoteToDurable(r.Context(), req.DurableKey, loc)
	}

	s.logger.Info("registered", lager.Data{"key": req.Key, "path": req.LocalPath, "durable_key": req.DurableKey})
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

	// Step 1: Check registry for explicit registration.
	//
	// resolveResponse.Source stays the ABSOLUTE path. It is response JSON the
	// ATC logs and surfaces, so it is an external contract, not an internal
	// representation — the one place in this handler where the ambient form is
	// the right answer rather than a leftover.
	sourceLoc, found := s.lookupRegistry(key)
	if found {
		sourcePath := s.registry.AmbientPath(sourceLoc)
		if err := s.copyArtifactGuarded(sourceLoc, dest); err != nil {
			logger.Error("copy-failed", err, lager.Data{"source": sourcePath})
			return resolveResponse{Status: "error", Source: sourcePath, Method: "local", Error: err.Error()}
		}
		duration := time.Since(start)
		logger.Info("resolved", lager.Data{"method": "registry", "source": sourcePath, "duration": duration.String()})
		return resolveResponse{Status: "ok", Source: sourcePath, Method: "registry", Duration: duration.String()}
	}

	// Step 2: Fallback — check if key maps to a steps/ directory on disk.
	stepsPath, keyErr := s.artifactLocation(filepath.Join(s.storagePath, "steps"), key)
	if keyErr != nil {
		return resolveResponse{Status: "error", Error: keyErr.Error()}
	}
	if info, err := os.Stat(stepsPath); err == nil && info.IsDir() {
		// Take the location Register stored rather than deriving a second one.
		stepsLoc, err := s.registry.Register(key, stepsPath)
		if err != nil {
			logger.Error("register-failed", err, lager.Data{"source": stepsPath})
			return resolveResponse{Status: "error", Source: stepsPath, Method: "filesystem", Error: err.Error()}
		}

		if err := s.copyArtifactGuarded(stepsLoc, dest); err != nil {
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
			if err := s.peers.Fetch(ctx, peerIP, key, dest); err != nil {
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

// handleResolve accepts POST /resolve with a JSON body containing {key, dest}.
// It looks up the artifact by key and copies it to the destination path.
//
// Resolution order:
//  1. Check local registry for an explicit registration
//  2. Fall back to filesystem scan (check if the key maps to a steps/ directory)
//  3. Query peer daemons for cross-node resolution
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req resolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}
	if req.Key == "" || req.Dest == "" {
		http.Error(w, "key and dest are required", http.StatusBadRequest)
		return
	}
	if err := validateRequestKey(req.Key); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateContainedPath(s.storagePath, req.Dest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !s.authorizeResolve(req) {
		s.logger.Info("resolve-unauthorized", lager.Data{"key": req.Key})
		http.Error(w, "resolve capability required", http.StatusForbidden)
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
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req batchResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Validate EVERY item before starting ANY of them. A partial batch that
	// refuses item 3 after item 1 has already copied is a side effect from a
	// refused request, which R5 forbids — and this endpoint is mTLS-exempt, so
	// it is the least authenticated way into resolveOne.
	for i, item := range req.Items {
		if err := validateRequestKey(item.Key); err != nil {
			http.Error(w, fmt.Sprintf("item %d: %v", i, err), http.StatusBadRequest)
			return
		}
		if err := validateContainedPath(s.storagePath, item.Dest); err != nil {
			http.Error(w, fmt.Sprintf("item %d: %v", i, err), http.StatusBadRequest)
			return
		}
		// Authorized in the same pass, for the same reason: refusing item 3
		// after item 1 has copied is a side effect from a refused request.
		if !s.authorizeResolve(item) {
			s.logger.Info("resolve-batch-unauthorized", lager.Data{"item": i, "key": item.Key})
			http.Error(w, fmt.Sprintf("item %d: resolve capability required", i), http.StatusForbidden)
			return
		}
	}

	results := make([]resolveResponse, len(req.Items))

	var wg sync.WaitGroup
	for i, item := range req.Items {
		wg.Add(1)
		go func(idx int, key, dest string) {
			defer wg.Done()
			// Bounded like the durable upload path. results[idx] is
			// index-assigned, so ordering and partial results survive.
			select {
			case s.resolveSem <- struct{}{}:
			case <-r.Context().Done():
				results[idx] = resolveResponse{Status: "error", Error: r.Context().Err().Error()}
				return
			}
			defer func() { <-s.resolveSem }()
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
// src is a location under the storage root; dest is an arbitrary absolute path
// on the node (a container's mount), which is why only one of the two is a
// RelKey.
func (s *Server) copyArtifactGuarded(src RelKey, dest string) error {
	release := s.guard.BeginRead(s.stepHandle(src))
	defer release()
	s.touchStepDir(src)

	// Serialise on the DESTINATION as well. The read guard above keys on the
	// source handle, so two copies with different sources and the same dest
	// were free to interleave os.RemoveAll(dest) and os.Rename — one would
	// delete the other's freshly-renamed tree. Reachable with legitimate keys,
	// so containment does not address it.
	// Refcounted so the map cannot grow without bound. The first version was a
	// bare sync.Map that stored an entry per distinct dest and never pruned —
	// 200 failed requests leaked 200 permanent entries, on an mTLS-exempt
	// endpoint whose dest length is bounded only by the body cap.
	key := filepath.Clean(dest)
	lock, _ := s.destLocks.LoadOrStore(key, &destLock{})
	dl := lock.(*destLock)
	dl.waiters.Add(1)
	dl.mu.Lock()
	defer func() {
		dl.mu.Unlock()
		if dl.waiters.Add(-1) == 0 {
			s.destLocks.CompareAndDelete(key, dl)
		}
	}()

	return s.copyArtifact(src, dest)
}

// copyArtifact copies the artifact at src into dest, atomically.
//
// Both sides go through a root handle: src through the storage root, dest
// through one handle on its parent. Every later step is relative to that
// handle, so a symlink swapped in mid-copy cannot redirect it.
//
// Copies land in a temp sibling and rename into place, so an interrupted copy
// leaves no partial state at dest to block the retry.
func (s *Server) copyArtifact(src RelKey, dest string) error {
	parent, base, err := openParent(dest)
	if err != nil {
		return err
	}
	defer parent.Close()

	tmp, err := mkdirTempIn(parent, ".cp-tmp-")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	tmpRoot, err := parent.OpenRoot(tmp)
	if err != nil {
		parent.RemoveAll(tmp)
		return fmt.Errorf("open temp dir: %w", err)
	}
	defer tmpRoot.Close()

	if err := copyTree(s.root, osName(src), tmpRoot); err != nil {
		parent.RemoveAll(tmp)
		return fmt.Errorf("copy %s -> %s: %w", src, dest, err)
	}

	parent.RemoveAll(base)
	if err := parent.Rename(tmp, base); err != nil {
		parent.RemoveAll(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, dest, err)
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
	// Before any return: capability is a property of this daemon, not of this
	// key, so it must be reported even on a miss or an error. Reporting it only
	// on a hit would mean it is known exactly when it is not needed.
	s.advertiseDurableTier(w)

	key, err := s.requestKey(r, "/resource-caches/")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	loc, found := s.lookupRegistry(key)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	// Verify the location still exists on disk — aliases can become stale if
	// the sweeper removed the step directory. Through the handle.
	if _, err := s.root.Stat(osName(loc)); err != nil {
		if os.IsNotExist(err) {
			s.registry.Remove(key)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "rel": string(loc)})
		w.WriteHeader(http.StatusInternalServerError)
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
	s.advertiseDurableTier(w)

	key, err := s.requestKey(r, "/resource-caches/")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	loc, found := s.lookupRegistry(key)
	if !found {
		http.NotFound(w, r)
		return
	}

	info, err := s.root.Stat(osName(loc))
	if err != nil {
		if os.IsNotExist(err) {
			s.registry.Remove(key)
			http.NotFound(w, r)
			return
		}
		s.logger.Error("resource-cache-stat-error", err, lager.Data{"key": key, "rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if s.nodeName != "" {
		w.Header().Set("X-Node-Name", s.nodeName)
	}

	// Same guard discipline as handleGetArtifact: no sweeps mid-stream.
	release := s.guard.BeginRead(s.stepHandle(loc))
	defer release()
	s.touchStepDir(loc)

	if info.IsDir() {
		w.Header().Set("Content-Type", "application/x-tar")
		if err := s.tarDirectory(w, loc); err != nil {
			s.logger.Error("failed-to-tar-resource-cache", err, lager.Data{"key": key, "rel": string(loc)})
			// Headers already sent; abort so the peer sees a hard failure
			// rather than a clean-looking truncated tar.
			panic(http.ErrAbortHandler)
		}
		return
	}

	f, err := s.root.Open(osName(loc))
	if err != nil {
		s.logger.Error("resource-cache-open-error", err, lager.Data{"key": key, "rel": string(loc)})
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	if _, err := io.Copy(w, f); err != nil {
		s.logger.Error("failed-to-stream-resource-cache", err, lager.Data{"key": key, "rel": string(loc)})
		panic(http.ErrAbortHandler)
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Root returns the storage root handle, so components constructed alongside the
// Server operate inside the same containment rather than opening their own.
func (s *Server) Root() *os.Root {
	return s.root
}
