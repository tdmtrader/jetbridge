package outputplane

// The output plane's protected, versioned control API.
//
// Two disjoint surfaces, and the disjointness is enforced rather than
// documented. /execution/v1/* is the base protocol's four closed operations;
// /capture/v1/* is the optional extension. Every route declares the facet it
// belongs to, and the capability a request carries must have been minted for
// that facet, that operation and that exact execution -- so a base control
// capability presented at a hold, a seal or a publish is refused however valid
// it is, and a capture capability presented at a base route is refused there.
//
// ROUTES ACCEPT IDS. Not paths, not buckets, not scopes, not object keys. The
// server derives every location from the identity it issued and from
// authenticated configuration. The one place a caller-chosen location is
// representable at all is PublicationRequest.Namespace, and it is there so that
// such a field is REFUSED WITH A MESSAGE rather than silently dropped: a
// hostile client really can put one in a request body, and the difference is
// whether an operator ever finds out.
//
// Base requests never require or synthesize an output or source field. A base
// caller sends an identity and gets a classification; nothing on that path
// mentions a hold, a bucket or a capture, which is decision F13's contract
// obligation expressed as a route table.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// CapabilityHeader carries the attenuated bearer capability for one operation.
const CapabilityHeader = "Hangar-Control-Capability"

// Server is the daemon's HTTP surface.
type Server struct {
	inputs     inputStages
	daemon     *Daemon
	base       *ExecutionLedger
	capture    *CaptureLedger
	capability *executioncontrol.CapabilityVerifier

	// unreadyBecause is non-empty when live control state could not be
	// validated at startup. Readiness fails while it is, and every control
	// route fails closed: a daemon that could not read its own ledger is not a
	// daemon that may answer questions about what it says.
	unreadyBecause string

	// spool bounds how many trees are being canonicalized or published at once.
	//
	// It is a bound on DISK rather than on CPU. Canonicalization spools a whole
	// tree into the scratch emptyDir, and that volume has a sizeLimit: exceeding
	// it evicts this Pod, and an emptyDir with no sizeLimit at all fills the
	// node's disk and evicts every Pod on the node. The number an operator can
	// reason about is concurrency times the maximum tree, so the concurrency is
	// configuration and the chart refuses a product that does not fit.
	//
	// Waiting is the right behaviour rather than refusing: a capture holds a
	// lease it renews, and the caller's own deadline is what bounds the wait.
	spool chan struct{}

	// mutualTLS is set when the daemon serves HTTPS with a client CA. It makes
	// every route but the node-local one require a verified client
	// certificate. It is a server fact rather than a per-request one because
	// "this request arrived over TLS" is not the same claim as "this daemon
	// requires TLS", and only the second one can be enforced.
	mutualTLS bool
	reads     *managedReads

	// daemonCertificate is the DER of the certificate this daemon SERVES with.
	// Every artifact daemon in a deployment serves with the same certificate
	// and presents it as its client certificate to its peers for mirroring
	// and cross-node fetches, so it verifies against the one CA. On this
	// plane's off-node routes it is refused: those are the control plane's,
	// and a node that could drive another node's capture plane with its own
	// serving certificate would make every node an authority over every
	// other. Nil refuses nothing extra.
	daemonCertificate []byte
}

// RequireClientCertificates turns on the client-certificate check for every
// route that is not node-local.
func (server *Server) RequireClientCertificates() { server.mutualTLS = true }

// RefuseDaemonCertificate makes the off-node routes refuse a caller that
// presents the daemons' own serving certificate. See daemonCertificate.
func (server *Server) RefuseDaemonCertificate(der []byte) { server.daemonCertificate = der }

// controlPlaneCaller reports whether an off-node request may proceed: with
// mTLS configured it must carry a verified client certificate, and that
// certificate must not be the daemons' own.
func (server *Server) controlPlaneCaller(request *http.Request) bool {
	if !server.mutualTLS {
		return true
	}
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return false
	}

	return len(server.daemonCertificate) == 0 ||
		!bytes.Equal(request.TLS.PeerCertificates[0].Raw, server.daemonCertificate)
}

func NewServer(daemon *Daemon, base *ExecutionLedger, capture *CaptureLedger,
	capability *executioncontrol.CapabilityVerifier, unreadyBecause string) *Server {
	return NewServerWithSpool(daemon, base, capture, capability, unreadyBecause, 1)
}

// NewServerWithSpool is NewServer with the scratch concurrency bound named.
func NewServerWithSpool(daemon *Daemon, base *ExecutionLedger, capture *CaptureLedger,
	capability *executioncontrol.CapabilityVerifier, unreadyBecause string,
	concurrency int) *Server {
	if concurrency < 1 {
		concurrency = 1
	}

	return &Server{
		daemon: daemon, base: base, capture: capture,
		capability: capability, unreadyBecause: unreadyBecause,
		spool: make(chan struct{}, concurrency),
	}
}

// spooling holds one of the scratch slots for the duration of the call.
//
// The context is honoured while waiting, so a caller whose own deadline expires
// in the queue gets its deadline's error rather than being admitted late into
// work nobody is waiting for any more.
func (server *Server) spooling(ctx context.Context) (func(), error) {
	select {
	case server.spool <- struct{}{}:
		return func() { <-server.spool }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: waiting for a canonicalization scratch slot: %v",
			output.ErrTimeout, ctx.Err())
	}
}

// route is one endpoint: its facet, its operation name, and what it does.
//
// facet and operation are data rather than something each handler remembers to
// check, because "the middleware checks the token is valid" and "the middleware
// checks the token is valid FOR THIS ROUTE" look identical at every call site
// and differ completely in what they permit.
type route struct {
	facet     executioncontrol.Facet
	operation string
	handle    func(*Server, http.ResponseWriter, *http.Request, executioncontrol.Identity) (any, error)

	// nodeLocal marks the one route whose caller is a container in a Pod on
	// this node rather than the control plane.
	//
	// Every other route on this API is now called by the ATC, which is on
	// another node, so when TLS is configured they require a verified client
	// certificate -- a bearer capability over plaintext off-node is
	// interceptable inside its TTL. The capture control init holds no client
	// certificate and cannot be given one (the task's Pod holds no
	// output-plane credential beyond its one-shot warrant), so its route stays
	// reachable without one, exactly as cmd/artifact-daemon exempts /resolve
	// for the same caller and the same reason. It is node-local traffic on the
	// node's own loopback or CNI path, and the warrant is still a signed,
	// facet-scoped, single-use capability.
	nodeLocal bool
}

// identified is how the middleware finds the execution a capability must be
// bound to, and it has to read TWO shapes.
//
// The base protocol's frozen types EMBED Identity, so an Envelope or a
// ClassifyRequest carries execution_id and fence at the top level. The
// extension's types carry it under `execution`, because they also carry an
// output and a step, and a flattened identity beside those would be
// ambiguous. Both are frozen, so the middleware accommodates both rather than
// either being changed to suit it.
//
// Reading the identity out of the BODY rather than a header is deliberate: the
// capability is bound to that identity, and a header the body could contradict
// would put the authorization key outside the thing being authorized.
type identified struct {
	Execution   *executioncontrol.Identity   `json:"execution"`
	ExecutionID executioncontrol.ExecutionID `json:"execution_id"`
	Fence       executioncontrol.Fence       `json:"fence"`
}

func (named identified) identity() executioncontrol.Identity {
	if named.Execution != nil {
		return *named.Execution
	}

	return executioncontrol.Identity{ExecutionID: named.ExecutionID, Fence: named.Fence}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /read/v1/stat", server.readStat)
	mux.HandleFunc("POST /read/v1/archive", server.readArchive)
	mux.HandleFunc("POST /read/v1/materialize", server.readMaterialize)
	mux.HandleFunc("POST /input/v1/stage", server.stageInput)
	mux.HandleFunc("POST /input/v1/publish", server.publishInput)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		// Liveness is not readiness. A daemon whose ledger is quarantined is
		// alive and must stay alive, so an operator can read the quarantine.
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if server.unreadyBecause != "" {
			http.Error(w, server.unreadyBecause, http.StatusServiceUnavailable)

			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /handshake", func(w http.ResponseWriter, request *http.Request) {
		// Node labels are hints; this is the authority. It says what this
		// daemon speaks and nothing about any execution, but its callers --
		// the web and the activation walk -- are off-node, and every off-node
		// route requires a verified client certificate when the daemon is
		// configured for one.
		if !server.controlPlaneCaller(request) {
			http.Error(w, "the control plane's verified client certificate is required for the handshake",
				http.StatusUnauthorized)

			return
		}
		// The pod's readiness is the artifact daemon's, not this plane's, so a
		// plane that could not read its own ledger says so HERE: a node that
		// cannot answer for its ledger must not claim to speak the protocol.
		if server.unreadyBecause != "" {
			http.Error(w, server.unreadyBecause, http.StatusServiceUnavailable)

			return
		}
		writeJSON(w, http.StatusOK, server.daemon.BaseHandshake())
	})
	mux.HandleFunc("GET /capture/v1/handshake", func(w http.ResponseWriter, request *http.Request) {
		// The extension handshake. It is the capture facet's, so a daemon
		// without the facet refuses it with the same typed result every other
		// capture route gives -- an empty handshake would be a daemon claiming
		// to speak a protocol it does not.
		if !server.daemon.OutputEnabled() {
			writeError(w, fmt.Errorf("%w: this daemon carries the base "+
				"exact-execution-control facet only, so it publishes nothing and has no "+
				"extension to describe", output.ErrCaptureDisabled))

			return
		}
		// It names a bucket and a derived namespace, which the base handshake
		// does not, so it is behind the same client-certificate requirement as
		// every other off-node call when the control API is configured for one.
		if !server.controlPlaneCaller(request) {
			http.Error(w, "the control plane's verified client certificate is required for the capture "+
				"extension handshake", http.StatusUnauthorized)

			return
		}
		if server.unreadyBecause != "" {
			http.Error(w, server.unreadyBecause, http.StatusServiceUnavailable)

			return
		}
		writeJSON(w, http.StatusOK, server.daemon.ExtensionHandshake())
	})

	for pattern, declared := range server.routes() {
		mux.Handle(pattern, server.protect(declared))
	}

	return mux
}

// routes is the whole table. Every entry names its facet; there is no entry
// that inherits one.
func (server *Server) routes() map[string]route {
	return map[string]route{
		// The base protocol's four closed operations. Nothing here mentions an
		// output, a hold or a bucket.
		// Admission and the supervisor's two writes. They are base routes and
		// they carry no output, source or capture field: this is the shape a
		// non-capture execution uses unchanged, which is decision F13's
		// contract obligation stated as a route rather than promised.
		"POST /execution/v1/admit":   {executioncontrol.BaseFacet, "admit", (*Server).admit, false},
		"POST /execution/v1/start":   {executioncontrol.BaseFacet, "start", (*Server).start, false},
		"POST /execution/v1/outcome": {executioncontrol.BaseFacet, "outcome", (*Server).outcome, false},

		"POST /execution/v1/classify": {executioncontrol.BaseFacet, "classify", (*Server).classify, false},
		// A read of the node's stored, signed start: what a control plane
		// that never retained it needs to interrupt and close the execution.
		"POST /execution/v1/start/inspect":    {executioncontrol.BaseFacet, "inspect-start", (*Server).inspectStart, false},
		"POST /execution/v1/observe":          {executioncontrol.BaseFacet, "observe", (*Server).observe, false},
		"POST /execution/v1/stop":             {executioncontrol.BaseFacet, "stop", (*Server).stop, false},
		"POST /execution/v1/cleanup-eligible": {executioncontrol.BaseFacet, "cleanup-eligible", (*Server).cleanupEligible, false},

		// The optional capture extension. Disjoint surface, disjoint facet.
		// Hold is the one node-local route: its caller is the capture control
		// init in the producing Pod, which holds no client certificate. Every
		// other one is the control plane's, off-node.
		"POST /capture/v1/hold":    {output.CaptureFacet, "hold", (*Server).hold, true},
		"POST /capture/v1/seal":    {output.CaptureFacet, "seal", (*Server).seal, false},
		"POST /capture/v1/publish": {output.CaptureFacet, "publish", (*Server).publish, false},
		"POST /capture/v1/release": {output.CaptureFacet, "release", (*Server).release, false},
		"POST /capture/v1/stat":    {output.CaptureFacet, "stat", (*Server).stat, false},
	}
}

// protect is the facet check, the replay refusal and the fail-closed gate.
func (server *Server) protect(declared route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if server.unreadyBecause != "" {
			// Fail closed. The output plane being unavailable never warrants authority,
			// and "unavailable" includes "cannot read its own ledger".
			http.Error(w, server.unreadyBecause, http.StatusServiceUnavailable)

			return
		}

		// The facet gate, and it is FIRST because it is the only refusal here
		// that is about this daemon rather than about this request. A
		// component without the capture facet refuses durable output capture
		// with a typed result and no cache-tier fallback. Answering "forbidden"
		// or "bad request" would tell a caller to fix the call; answering 501
		// tells it this daemon does not do this, which is the one answer that
		// does not produce a retry.
		if declared.facet == output.CaptureFacet && !server.daemon.OutputEnabled() {
			writeError(w, fmt.Errorf("%w: the %s operation needs the durable-capture "+
				"extension, and this daemon carries the base exact-execution-control facet "+
				"only", output.ErrCaptureDisabled, declared.operation))

			return
		}

		// The transport check comes before the capability check, and that
		// order is the point: a capability presented over an unauthenticated
		// transport has already been on the wire in the clear, and verifying
		// it would be deciding whether to honour a token that may have been
		// copied on the way in.
		if !declared.nodeLocal && !server.controlPlaneCaller(request) {
			http.Error(w, "the control plane's verified client certificate is required for its "+
				"operations on this daemon; only the node-local capture hold is exempt",
				http.StatusUnauthorized)

			return
		}

		body, err := readBody(request)
		if err != nil {
			writeError(w, err)

			return
		}

		var named identified
		if err := json.Unmarshal(body, &named); err != nil {
			writeError(w, fmt.Errorf("%w: the request body does not name an execution: %v",
				output.ErrIncomplete, err))

			return
		}
		identity := named.identity()
		if err := identity.Validate(); err != nil {
			writeError(w, err)

			return
		}

		// The claims are the ROUTE's, not the token's. A verifier that decoded
		// the facet out of the capability and then checked the capability
		// against it would authorize every facet.
		if err := server.capability.Verify(
			executioncontrol.ControlCapability(request.Header.Get(CapabilityHeader)),
			executioncontrol.CapabilityClaims{
				Facet:           declared.facet,
				Operation:       declared.operation,
				Identity:        identity,
				ActivationEpoch: server.daemon.ActivationEpoch(),
			}); err != nil {
			writeError(w, fmt.Errorf("%w: %s facet, %s operation: %v",
				output.ErrUnauthorized, declared.facet, declared.operation, err))

			return
		}

		request = request.WithContext(request.Context())
		request.Body = readerOf(body)

		answer, err := declared.handle(server, w, request, identity)
		if err != nil {
			writeError(w, err)

			return
		}
		writeJSON(w, http.StatusOK, answer)
	})
}

func (server *Server) admit(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var envelope executioncontrol.Envelope
	if err := decode(request, &envelope); err != nil {
		return nil, err
	}
	if err := server.base.Admit(envelope); err != nil {
		return nil, err
	}

	return server.base.Classify(envelope.Identity)
}

// startRequest and outcomeRequest are the supervisor's two writes.
//
// They are the only routes on the base surface that change anything about a
// process, and both are written durably before their caller may act: the start
// before the child is launched, the outcome before the result is exposed.
type startRequest struct {
	Execution       executioncontrol.Identity        `json:"execution"`
	PodUID          executioncontrol.PodUID          `json:"pod_uid"`
	ProcessIdentity executioncontrol.ProcessIdentity `json:"process_identity"`
}

type outcomeRequest struct {
	Execution executioncontrol.Identity            `json:"execution"`
	Kind      executioncontrol.AcknowledgementKind `json:"kind"`
	Outcome   executioncontrol.ExitOutcome         `json:"outcome"`
}

func (server *Server) start(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var started startRequest
	if err := decode(request, &started); err != nil {
		return nil, err
	}

	return server.base.RecordStart(started.Execution, started.PodUID, started.ProcessIdentity)
}

func (server *Server) outcome(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var recorded outcomeRequest
	if err := decode(request, &recorded); err != nil {
		return nil, err
	}

	return server.base.RecordOutcome(recorded.Execution, recorded.Kind, recorded.Outcome)
}

func (server *Server) inspectStart(_ http.ResponseWriter, _ *http.Request,
	identity executioncontrol.Identity) (any, error) {
	return server.base.InspectStart(identity)
}

func (server *Server) classify(_ http.ResponseWriter, _ *http.Request,
	identity executioncontrol.Identity) (any, error) {
	return server.base.Classify(identity)
}

func (server *Server) observe(_ http.ResponseWriter, _ *http.Request,
	identity executioncontrol.Identity) (any, error) {
	return server.base.Observe(identity)
}

func (server *Server) stop(_ http.ResponseWriter, _ *http.Request,
	identity executioncontrol.Identity) (any, error) {
	return server.base.RequestStop(identity)
}

func (server *Server) cleanupEligible(_ http.ResponseWriter, _ *http.Request,
	identity executioncontrol.Identity) (any, error) {
	return server.base.CleanupEligible(identity)
}

func (server *Server) hold(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var held output.CaptureHoldRequest
	if err := decode(request, &held); err != nil {
		return nil, err
	}
	if held.Execution != identity {
		return nil, fmt.Errorf("%w: the hold is authorized for another execution", output.ErrUnauthorized)
	}

	return server.capture.Hold(request.Context(), held)
}

// seal holds a scratch slot for its canonicalization. Its wait for the Pod's
// containers to stop happens first, outside the slot, so a long-running
// sidecar does not starve every other capture of scratch.
func (server *Server) seal(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var sealed output.CaptureSealRequest
	if err := decode(request, &sealed); err != nil {
		return nil, err
	}
	if sealed.Execution != identity {
		return nil, fmt.Errorf("%w: the seal is authorized for another execution", output.ErrUnauthorized)
	}
	return server.capture.Seal(request.Context(), sealed, server.spooling)
}

func (server *Server) publish(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var publication output.CapturePublishRequest
	if err := decode(request, &publication); err != nil {
		return nil, err
	}
	if publication.Execution != identity {
		return nil, fmt.Errorf("%w: the publish is authorized for another execution", output.ErrUnauthorized)
	}
	return server.capture.Publish(request.Context(), publication, server.spooling)
}

func (server *Server) release(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var released output.CaptureReleaseRequest
	if err := decode(request, &released); err != nil {
		return nil, err
	}
	if released.Execution != identity {
		return nil, fmt.Errorf("%w: the release is authorized for another execution", output.ErrUnauthorized)
	}

	return server.capture.Release(request.Context(), released)
}

func (server *Server) stat(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var stat output.CaptureStatRequest
	if err := decode(request, &stat); err != nil {
		return nil, err
	}
	if stat.Execution != identity {
		return nil, fmt.Errorf("%w: the stat is authorized for another execution", output.ErrUnauthorized)
	}

	return server.capture.Stat(request.Context(), stat)
}

// writeError maps a typed refusal onto a status and a body that NAMES it.
//
// The status alone is not the answer. A caller has to be able to tell a
// collision from an unauthorized scope from a source that is sealed, because
// each one means a different next step, and "409" means all three.
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, output.ErrUnauthorized), errors.Is(err, executioncontrol.ErrUnauthorized):
		status = http.StatusForbidden
	case errors.Is(err, output.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, output.ErrSealed), errors.Is(err, output.ErrConflict),
		errors.Is(err, output.ErrGenerationConflict), errors.Is(err, executioncontrol.ErrStaleFence):
		status = http.StatusConflict
	case errors.Is(err, output.ErrInProgress):
		// Accepted: the operation is running in the background. The body names
		// it so a caller can tell it from a completed one.
		state := "publishing"
		if errors.Is(err, output.ErrSealInProgress) {
			state = "sealing"
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"state": state, "error": err.Error()})

		return
	case errors.Is(err, output.ErrSealUnconfirmed), errors.Is(err, output.ErrUnresolved):
		status = http.StatusPreconditionFailed
	case errors.Is(err, output.ErrIncomplete), errors.Is(err, output.ErrInvalidIdentity),
		errors.Is(err, output.ErrUnknownMember), errors.Is(err, executioncontrol.ErrIncomplete),
		errors.Is(err, executioncontrol.ErrInvalidIdentity),
		errors.Is(err, executioncontrol.ErrUnknownMember):
		status = http.StatusBadRequest
	case errors.Is(err, output.ErrUnsupportedProtocol), errors.Is(err, executioncontrol.ErrUnsupportedProtocol):
		status = http.StatusNotAcceptable
	case errors.Is(err, output.ErrCaptureDisabled):
		// 501 and not 403 or 404. "This component does not implement the
		// capture extension" is a statement about the daemon; a caller that
		// read it as "not permitted" or "no such route" would retry, and the
		// one retry that must never happen is falling back to the cache tier.
		status = http.StatusNotImplemented
	case errors.Is(err, output.ErrInfrastructure), errors.Is(err, output.ErrCorrupt):
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// maxControlRequestBytes bounds a control body. These are identities and
// fences; a megabyte of them is a caller doing something else.
const maxControlRequestBytes = 1 << 20

func readBody(request *http.Request) ([]byte, error) {
	defer request.Body.Close()

	body, err := io.ReadAll(http.MaxBytesReader(nil, request.Body, maxControlRequestBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: reading the control request: %v", output.ErrIncomplete, err)
	}

	return body, nil
}

func decode(request *http.Request, into any) error {
	body, err := readBody(request)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	// Unknown fields are refused rather than dropped. A field this daemon
	// cannot read is a fact it would silently ignore, and on this API the
	// facts are what authorize things.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("%w: decoding the control request: %v", output.ErrIncomplete, err)
	}

	return nil
}

// readerOf lets the body be read twice: once by the middleware, to learn which
// execution the capability must be checked against, and once by the handler.
// A control request is bounded above, so buffering it is cheap and the
// alternative -- trusting a header for the identity the token is bound to --
// would put the authorization key outside the signed body.
func readerOf(body []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(body)) }
