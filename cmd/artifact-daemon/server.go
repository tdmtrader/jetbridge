package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/artifactcap"
	"github.com/concourse/concourse/artifactwire"
	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"
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

	// outputPlane serves the output plane's routes (outputplane.Patterns) on
	// this daemon's listener. nil keeps them absent.
	outputPlane http.Handler

	// sourceLedger reads the output plane's step markers before destructive
	// operations. It is initialized even when outputPlane is absent, without
	// creating the control directory. An unreadable ledger refuses destruction.
	sourceLedger *outputplane.Classifier

	// resolveSem bounds concurrent resolves NODE-WIDE, across every request on
	// the mTLS-exempt route (/resolve-batch): one channel per process. Each resolve
	// copies a whole artifact, and before the bound existed the batch endpoint
	// spawned one goroutine per item with no cap. The work a slot holds is
	// cancellable (the copy checks its request context per entry), so a caller
	// that gives up releases the slot instead of pinning it for the whole copy.
	resolveSem chan struct{}
	// destLocks serialises resolves by DESTINATION. The read guard inside
	// copyArtifactGuarded keys on the SOURCE handle, so two items with
	// different keys and the same dest raced on RemoveAll(dest) and Rename —
	// reachable with entirely legitimate keys, and equally through the peer
	// branch, which is why resolveOne takes the lock around the WHOLE
	// resolution rather than copyArtifactGuarded taking it around one copy.
	//
	// destLocksMu guards the map AND each entry's waiter count together, so
	// that registering a waiter and deleting an idle entry are mutually
	// exclusive. A lock-free refcount cannot make them so — the decrement and
	// the delete are separate steps, and a newcomer can register on an entry a
	// releaser has already decided to delete, leaving two holders of one dest.
	destLocksMu sync.Mutex
	destLocks   map[string]*destLock

	// root is the handle every filesystem operation on artifact data goes
	// through. Containment is a property of the handle, not of a check
	// performed before the operation: os.Root refuses to resolve a path out of
	// itself, so there is no ordering to get wrong and no name to out-think.
	root *os.Root

	// resolveVerifier authenticates the mTLS-exempt resolve route.
	resolveVerifier *artifactcap.Verifier
}

// destLock serialises resolves to one destination. Its 1-slot channel is the
// lock itself, so a waiter can give up when its request context dies — a
// sync.Mutex wait is uninterruptible, and on the sem-bounded resolve path an
// abandoned request would otherwise pin a semaphore slot for the whole copy.
//
// waiters is guarded by Server.destLocksMu, NOT atomic: it is read and written
// only while that mutex is held, which is exactly what lets "delete when idle"
// and "register a waiter" be one indivisible decision.
type destLock struct {
	ch      chan struct{}
	waiters int
}

// maxConcurrentBatchResolves caps in-flight copies from one batch request.
// Production batches are one item per step input — single digits — against a
// 180s per-attempt timeout, so this is invisible to legitimate traffic.
const maxConcurrentBatchResolves = 4

// maxJSONBodyBytes caps the JSON control-plane bodies. Deliberately NOT applied
// to PUT /stream-in/, which streams whole artifacts: every mirror push and ATC
// upload goes through it, and a cap there would break artifact delivery
// outright.
const maxJSONBodyBytes = 1 << 20

// NewServer creates a new artifact-daemon server.
// NewServer can now fail: it acquires the storage root at construction.
//
// Deliberately at construction rather than lazily. A lazy open converts a boot
// failure into a per-request failure and makes "every operation goes through
// the handle" conditional on something having succeeded earlier; opening it in
// Handler() cannot work because Mirror and Sweeper are built
// outside it and need the same root. resolveKey is the raw resolve capability
// key; a daemon without one cannot authorize a resolve, so it does not start.
func NewServer(logger lager.Logger, storagePath, nodeName string, resolveKey []byte) (*Server, error) {
	verifier, err := artifactcap.NewVerifier(resolveKey)
	if err != nil {
		return nil, fmt.Errorf("resolve capability key: %w", err)
	}
	root, err := os.OpenRoot(storagePath)
	if err != nil {
		return nil, fmt.Errorf("open storage root %q: %w", storagePath, err)
	}

	// Every destructive component shares this classifier, independently of
	// whether the output plane's routes are configured.
	classifier := outputplane.NewClassifier(storagePath)

	registry := NewRegistry(logger, storagePath)
	registry.SetSourceLedger(classifier)

	return &Server{
		logger:      logger,
		storagePath: storagePath,
		nodeName:    nodeName,
		registry:    registry,
		metrics:     newMetrics(),
		guard:       NewReadGuard(),
		resolveSem:  make(chan struct{}, maxConcurrentBatchResolves),
		destLocks:   make(map[string]*destLock),
		root:        root,

		resolveVerifier: verifier,

		// The classifier is always attached, and it is attached here rather
		// than behind a flag on purpose: a node that gained an output plane
		// after this one started would otherwise keep destroying held sources
		// until somebody restarted it. New() does not touch the filesystem;
		// the absence of a control directory is a real answer meaning "no
		// output plane on this node", and it is answered on every call.
		sourceLedger: classifier,
	}, nil
}

// SetOutputPlane mounts the output plane's routes on this daemon's handler.
// The plane does its own per-route client-certificate check, because some of
// its routes are node-local and others are not.
func (s *Server) SetOutputPlane(plane http.Handler) {
	s.outputPlane = plane
}

// Guard returns the read/sweep coordination guard. The sweeper takes its
// exclusive side per directory removal so reads never copy from a directory
// being deleted.
func (s *Server) Guard() *ReadGuard {
	return s.guard
}

// SourceLedger exposes the read-only output-plane classifier so components
// that destroy things outside this type -- the Sweeper -- ask the same
// question the handlers do, of the same reader.
func (s *Server) SourceLedger() *outputplane.Classifier {
	return s.sourceLedger
}

// stepHandle returns the guard key for a location: the {handle} segment for
// anything under steps/, or the location itself for a registered location
// elsewhere under the root, so it still gets a consistent per-location lock.
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
func (s *Server) Registry() *Registry {
	return s.registry
}

// authorizeResolve requires every resolve to carry a capability bound to its
// exact key and destination, and fails CLOSED: a missing or unverifiable
// capability is refused.
//
// /resolve-batch is mTLS-EXEMPT by design: the init container dials the
// daemon by node IP, which cannot be a certificate SAN. The capability is
// therefore the only authentication this route has — and it takes a
// caller-supplied dest that becomes a RemoveAll and a Rename. Containment does
// not rest on it: validateResolveDest refuses a destination that names the
// store's shape before the capability is checked.
func (s *Server) authorizeResolve(req artifactwire.ResolveRequest) bool {
	if req.Capability == "" {
		return false
	}
	return s.resolveVerifier.VerifyResolve(req.Capability, req.Key, req.Dest, time.Now()) == nil
}

// SetPeerResolver configures the peer resolver for cross-node artifact
// resolution. When nil, /resolve-batch only checks local storage.
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
// (/healthz, /resolve-batch) are accessible without a client cert.
func (s *Server) Handler(opts ...HandlerOption) http.Handler {
	cfg := handlerConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	mux := http.NewServeMux()

	// protect wraps a handler with mTLS enforcement when TLS is enabled.
	protect := func(h http.HandlerFunc) http.HandlerFunc {
		if cfg.tlsEnabled {
			return s.requireClientCert(h)
		}
		return h
	}

	// Every route comes from the wire module's table, which also says whether
	// it is mTLS-exempt. Exempt routes are the ones a kubelet probe, a
	// Prometheus scraper or an init container in a pod on this node has to
	// reach, none of which holds a client certificate; what protects each one
	// is stated at its handler.
	handle := func(route artifactwire.Route, h http.HandlerFunc) {
		if route.MTLSExempt {
			mux.HandleFunc(route.Pattern(), h)
		} else {
			mux.HandleFunc(route.Pattern(), protect(h))
		}
	}

	handle(artifactwire.Healthz, s.handleHealthz)
	handle(artifactwire.ResolveBatch, s.handleResolveBatch)
	// Read-only, and mTLS-exempt for the same reason /resolve-batch is: the caller
	// is an init container in a pod on this node, it holds no client
	// certificate, and the question it needs answered is about its OWN step
	// directory. It reads nothing but the class, writes nothing, and names no
	// path the caller did not already name.
	handle(artifactwire.CaptureHeld, s.handleCaptureClass)
	if s.metrics != nil {
		mux.Handle(artifactwire.Metrics.Pattern(), s.metrics.handler())
	}

	handle(artifactwire.GetArtifact, s.handleGetArtifact)
	handle(artifactwire.DeleteArtifact, s.handleDeleteArtifact)
	handle(artifactwire.HeadArtifact, s.handleHeadArtifact)
	handle(artifactwire.Register, s.handleRegister)
	handle(artifactwire.Mirror, s.handleMirrorTrigger)
	handle(artifactwire.StreamIn, s.handleStreamIn)
	handle(artifactwire.HeadResourceCache, s.handleHeadResourceCache)
	handle(artifactwire.GetResourceCache, s.handleGetResourceCache)
	if s.outputPlane != nil {
		for _, pattern := range outputplane.Patterns {
			mux.Handle(pattern, s.outputPlane)
		}
	} else {
		// A daemon without the output plane is ready when it serves.
		mux.HandleFunc("GET /readyz", s.handleHealthz)
	}

	return mux
}

// MetricsHandler serves /metrics and nothing else. It is the handler for the
// plain-HTTP listener --metrics-port opens: a Prometheus scraper holds no
// client certificate and no CA for the daemon's TLS port, and a listener that
// can only answer /metrics hands it nothing a certificate would have guarded.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	if s.metrics != nil {
		mux.Handle(artifactwire.Metrics.Pattern(), s.metrics.handler())
	}
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

// refuse is THE way a handler turns a request away: it counts, logs, and
// replies. Not a convenience wrapper — before it existed, 24 of ~30 refusal
// sites wrote an http.Error and nothing else, so a build could fail on a
// daemon-side refusal with no daemon-side trace of it at all. Every new
// refusal this series added was silent in exactly that way.
//
// reason must come from the bounded set in containment.go; it is a metric
// label. The client still sees err's text where the site used to pass it, so
// the reply is unchanged.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, reason string, err error) {
	route := refusalRoute(r)
	s.metrics.recordRefusal(route, reason)

	data := lager.Data{"route": route, "reason": reason, "status": status}
	msg := reason
	if err != nil {
		data["detail"] = err.Error()
		msg = err.Error()
	}
	s.logger.Info("refused", data)
	http.Error(w, msg, status)
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
// needed by the read/sweep guard (whose keys stay absolute) and by
// tarDirectory (which walks a path; egress belongs to another track), and
// artifactPath above produces it — routed through artifactLocation so the
// walked path keeps the containment check the walker itself does not have.
func (s *Server) artifactKey(r *http.Request) (string, error) {
	key, err := s.requestKey(r, artifactwire.ArtifactsPrefix)
	if err != nil {
		return "", err
	}
	if _, err := s.artifactLocation(s.storagePath, key); err != nil {
		return "", err
	}
	return key, nil
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
