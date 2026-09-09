package main

// The output daemon's protected, versioned control API.
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
// mentions a hold, a bucket or a receipt, which is decision F13's contract
// obligation expressed as a route table.

import (
	"bytes"
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
	daemon     *Daemon
	base       *ExecutionLedger
	source     *SourceLedger
	capability *executioncontrol.CapabilityVerifier

	// unreadyBecause is non-empty when live control state could not be
	// validated at startup. Readiness fails while it is, and every control
	// route fails closed: a daemon that could not read its own ledger is not a
	// daemon that may answer questions about what it says.
	unreadyBecause string

	// mutualTLS is set when the daemon serves HTTPS with a client CA. It makes
	// every route but the node-local one require a verified client
	// certificate. It is a server fact rather than a per-request one because
	// "this request arrived over TLS" is not the same claim as "this daemon
	// requires TLS", and only the second one can be enforced.
	mutualTLS bool
}

// RequireClientCertificates turns on the client-certificate check for every
// route that is not node-local.
func (server *Server) RequireClientCertificates() { server.mutualTLS = true }

func NewServer(daemon *Daemon, base *ExecutionLedger, source *SourceLedger,
	capability *executioncontrol.CapabilityVerifier, unreadyBecause string) *Server {
	return &Server{
		daemon: daemon, base: base, source: source,
		capability: capability, unreadyBecause: unreadyBecause,
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
	// certificate and cannot be given one (Req 24 gives the task's Pod no
	// output-plane credential beyond its one-shot grant), so its route stays
	// reachable without one, exactly as cmd/artifact-daemon exempts /resolve
	// for the same caller and the same reason. It is node-local traffic on the
	// node's own loopback or CNI path, and the grant is still a signed,
	// facet-scoped, single-use capability.
	nodeLocal bool
}

// identified is how the middleware finds the execution a capability must be
// bound to, and it has to read TWO shapes.
//
// The base protocol's frozen types EMBED Identity, so an Envelope or a
// ClassifyRequest carries execution_id and fence at the top level. The
// extension's types carry it under `execution`, because they also carry a
// handoff and an incarnation and a flattened identity beside those would be
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
	mux.HandleFunc("GET /handshake", func(w http.ResponseWriter, _ *http.Request) {
		// Node labels are hints; this is the authority. It is unauthenticated
		// on purpose: it says what this daemon speaks and nothing about any
		// execution.
		writeJSON(w, http.StatusOK, executioncontrol.Handshake{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			LedgerVersion:   executioncontrol.LedgerVersion,
			ControlKeyID:    server.daemon.ControlKeyID(),
			ActivationEpoch: server.daemon.Namespace().ActivationEpoch(),
		})
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
		// output, a hold, a bucket or a receipt.
		// Admission and the supervisor's two writes. They are base routes and
		// they carry no output, source or capture field: this is the shape a
		// non-capture execution uses unchanged, which is decision F13's
		// contract obligation stated as a route rather than promised.
		"POST /execution/v1/admit":   {executioncontrol.BaseFacet, "admit", (*Server).admit, false},
		"POST /execution/v1/start":   {executioncontrol.BaseFacet, "start", (*Server).start, false},
		"POST /execution/v1/outcome": {executioncontrol.BaseFacet, "outcome", (*Server).outcome, false},

		"POST /execution/v1/classify":         {executioncontrol.BaseFacet, "classify", (*Server).classify, false},
		"POST /execution/v1/observe":          {executioncontrol.BaseFacet, "observe", (*Server).observe, false},
		"POST /execution/v1/stop":             {executioncontrol.BaseFacet, "stop", (*Server).stop, false},
		"POST /execution/v1/cleanup-eligible": {executioncontrol.BaseFacet, "cleanup-eligible", (*Server).cleanupEligible, false},

		// The optional capture extension. Disjoint surface, disjoint facet.
		"POST /capture/v1/hold":                {output.CaptureFacet, "hold", (*Server).hold, true},
		"POST /capture/v1/hold/inspect":        {output.CaptureFacet, "inspect-hold", (*Server).inspectHold, false},
		"POST /capture/v1/writer-ticket":       {output.CaptureFacet, "issue-writer-ticket", (*Server).issueTicket, false},
		"POST /capture/v1/writer-ticket/close": {output.CaptureFacet, "close-writer-ticket", (*Server).closeTicket, false},
		"POST /capture/v1/seal":                {output.CaptureFacet, "begin-seal", (*Server).beginSeal, false},
		"POST /capture/v1/seal/confirm":        {output.CaptureFacet, "confirm-seal", (*Server).confirmSeal, false},
		"POST /capture/v1/seal/inspect":        {output.CaptureFacet, "inspect-seal", (*Server).inspectSeal, false},
		"POST /capture/v1/release":             {output.CaptureFacet, "release-hold", (*Server).release, false},
		"POST /capture/v1/publish":             {output.CaptureFacet, "publish", (*Server).publish, false},
		"POST /capture/v1/stat":                {output.CaptureFacet, "stat", (*Server).statExact, false},
	}
}

// protect is the facet check, the replay refusal and the fail-closed gate.
func (server *Server) protect(declared route) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if server.unreadyBecause != "" {
			// Fail closed. Output-daemon unavailability never grants authority,
			// and "unavailable" includes "cannot read its own ledger".
			http.Error(w, server.unreadyBecause, http.StatusServiceUnavailable)

			return
		}

		// The transport check comes before the capability check, and that
		// order is the point: a capability presented over an unauthenticated
		// transport has already been on the wire in the clear, and verifying
		// it would be deciding whether to honour a token that may have been
		// copied on the way in.
		if server.mutualTLS && !declared.nodeLocal &&
			(request.TLS == nil || len(request.TLS.PeerCertificates) == 0) {
			http.Error(w, "a verified client certificate is required for the control plane's "+
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
				ActivationEpoch: server.daemon.Namespace().ActivationEpoch(),
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
	_ executioncontrol.Identity) (any, error) {
	var admission output.CaptureAdmission
	if err := decode(request, &admission); err != nil {
		return nil, err
	}

	// The second argument is the interface's, and it is deliberately the zero
	// value: a caller offering an incarnation is offering a name it chose.
	return server.source.AcknowledgeHold(request.Context(), admission, output.SourceIncarnation{})
}

// holdQuery is the inspect routes' body. It names ids and nothing else, which
// is the rule this whole file exists to make true.
type holdQuery struct {
	Execution executioncontrol.Identity `json:"execution"`
	HandoffID output.HandoffID          `json:"handoff_id"`
}

func (server *Server) inspectHold(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var query holdQuery
	if err := decode(request, &query); err != nil {
		return nil, err
	}

	// The identity is the MIDDLEWARE's -- the one the capability was verified
	// against -- and not the one this handler re-reads out of the body. They
	// are the same field today; passing the checked one is what keeps them the
	// same when the body grows another way to name an execution.
	return server.source.InspectHold(query.HandoffID, identity)
}

func (server *Server) issueTicket(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var admission output.WriterAdmission
	if err := decode(request, &admission); err != nil {
		return nil, err
	}

	return server.source.AdmitWriter(request.Context(), admission)
}

func (server *Server) closeTicket(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var admission output.WriterAdmission
	if err := decode(request, &admission); err != nil {
		return nil, err
	}

	return server.source.RetireWriter(request.Context(), admission)
}

func (server *Server) beginSeal(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var sealRequest output.SealRequest
	if err := decode(request, &sealRequest); err != nil {
		return nil, err
	}

	return server.source.BeginSeal(request.Context(), sealRequest)
}

// sealConfirmation is the ATC's half on the wire: the drain and
// container-termination evidence for the set BeginSeal captured.
//
// The execution is named separately because the capability is bound to it, and
// the middleware must be able to read it out of the body before any of this is
// decoded.
type sealConfirmation struct {
	Execution executioncontrol.Identity `json:"execution"`
	Started   output.SealStarted        `json:"started"`
	Drained   []output.DrainedWriter    `json:"drained"`

	CaptureFence output.CaptureFence `json:"capture_fence"`
	ObservedAt   output.Timestamp    `json:"observed_at"`
}

func (server *Server) confirmSeal(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var confirmation sealConfirmation
	if err := decode(request, &confirmation); err != nil {
		return nil, err
	}

	// ConfirmSeal reads its execution out of the SealStarted the CALLER
	// supplied, which is the statement this confirmation acts on. The
	// capability was minted for the identity the middleware checked. Binding
	// them here is what stops a capability for one execution from confirming
	// another's seal -- the statement's signature is checked further down, so
	// the attacker is not a stranger, but "somebody who has seen A's seal
	// statement" is not "A".
	if confirmation.Started.Acknowledgement.Execution != identity {
		return nil, fmt.Errorf("%w: this confirmation is authorized for execution %s at fence "+
			"%d and the seal statement it carries is execution %s's at fence %d",
			output.ErrUnauthorized, identity.ExecutionID, identity.Fence,
			confirmation.Started.Acknowledgement.Execution.ExecutionID,
			confirmation.Started.Acknowledgement.Execution.Fence)
	}

	return server.source.ConfirmSeal(request.Context(), output.SealConfirmation{
		Started:      confirmation.Started,
		Drained:      confirmation.Drained,
		CaptureFence: confirmation.CaptureFence,
		ObservedAt:   confirmation.ObservedAt,
	})
}

func (server *Server) inspectSeal(_ http.ResponseWriter, request *http.Request,
	identity executioncontrol.Identity) (any, error) {
	var query holdQuery
	if err := decode(request, &query); err != nil {
		return nil, err
	}

	return server.source.InspectSeal(query.HandoffID, identity)
}

func (server *Server) release(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var intent output.ReleaseIntent
	if err := decode(request, &intent); err != nil {
		return nil, err
	}

	return server.source.AcknowledgeRelease(request.Context(), intent)
}

func (server *Server) publish(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var publication output.PublicationRequest
	if err := decode(request, &publication); err != nil {
		return nil, err
	}

	return server.PublishSealedTree(request.Context(), publication)
}

func (server *Server) statExact(_ http.ResponseWriter, request *http.Request,
	_ executioncontrol.Identity) (any, error) {
	var attestation struct {
		Execution executioncontrol.Identity `json:"execution"`
		Challenge output.StatChallenge      `json:"challenge"`
		Claims    output.ReceiptClaims      `json:"claims"`
	}
	if err := decode(request, &attestation); err != nil {
		return nil, err
	}
	receipt, _, err := server.daemon.StatExact(request.Context(), attestation.Challenge, attestation.Claims)

	return receipt, err
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
	case errors.Is(err, output.ErrSealUnconfirmed), errors.Is(err, output.ErrUnresolved):
		status = http.StatusPreconditionFailed
	case errors.Is(err, output.ErrIncomplete), errors.Is(err, output.ErrInvalidIdentity),
		errors.Is(err, output.ErrUnknownMember), errors.Is(err, executioncontrol.ErrIncomplete),
		errors.Is(err, executioncontrol.ErrInvalidIdentity),
		errors.Is(err, executioncontrol.ErrUnknownMember):
		status = http.StatusBadRequest
	case errors.Is(err, output.ErrUnsupportedProtocol), errors.Is(err, executioncontrol.ErrUnsupportedProtocol):
		status = http.StatusNotAcceptable
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
