package jetbridge

// The ATC's client for the output daemon's control API.
//
// This is the first OFF-NODE caller of that API. Phase 3's daemon listened on
// 127.0.0.1 with no TLS, which was the right shape while every caller was a pod
// on the same node; the web pod is not, and a bearer capability over plaintext
// off-node is interceptable inside its TTL. So this client speaks the same mTLS
// the ATC already speaks to the artifact daemon -- same certificate, same CA,
// same predicate (DaemonTLSConfigured) -- and the daemon requires a client
// certificate on every route except the one the in-pod control init calls,
// exactly as the artifact daemon exempts /resolve.
//
// It is ONE adapter. The base protocol's four closed operations and the two
// supervisor writes go through the same client as the capture extension's hold
// and writer tickets, because there must be exactly one owner of an execution's
// control state and a second http.Client would be a second owner in waiting.
// The sibling `exact_execution_control` track extends this adapter; it does not
// replace it, which is why nothing here is capture-shaped: every capture
// operation takes the extension's own types and the base ones take none of
// them.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// OutputControl is what the runtime needs from the output daemon.
//
// The four base operations are Classify, Observe, RequestStop and
// CleanupEligible -- the protocol's closed set. Admit, RecordStart and
// RecordOutcome are the writes admission and the supervisor make. The rest is
// the capture extension.
type OutputControl interface {
	Admit(ctx context.Context, envelope executioncontrol.Envelope) (executioncontrol.ClassifyResult, error)
	Classify(ctx context.Context, id executioncontrol.Identity) (executioncontrol.ClassifyResult, error)
	RecordStart(ctx context.Context, id executioncontrol.Identity, pod executioncontrol.PodUID,
		process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error)
	RecordOutcome(ctx context.Context, id executioncontrol.Identity,
		kind executioncontrol.AcknowledgementKind,
		outcome executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error)
	Observe(ctx context.Context, id executioncontrol.Identity,
		wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error)
	RequestStop(ctx context.Context,
		id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error)
	CleanupEligible(ctx context.Context,
		id executioncontrol.Identity) (executioncontrol.DestructiveCleanupEligibleResult, error)

	// ReserveIncarnation asks the daemon for the location this capture will
	// hold, BEFORE the producing Pod is built. It is the ATC's only source of
	// that path: nothing here composes one.
	ReserveIncarnation(ctx context.Context,
		admission output.CaptureAdmission) (output.ReservedIncarnation, error)
	InspectHold(ctx context.Context, id executioncontrol.Identity,
		handoff output.HandoffID) (output.CaptureAcknowledgement, error)
	AdmitWriter(ctx context.Context,
		admission output.WriterAdmission) (output.CaptureAcknowledgement, error)
	RetireWriter(ctx context.Context,
		admission output.WriterAdmission) (output.CaptureAcknowledgement, error)
	InspectWriter(ctx context.Context, id executioncontrol.Identity,
		handoff output.HandoffID, ticket output.WriterTicketID) (output.WriterInspection, error)

	// The publication half, which the control plane's capture coordinator
	// drives. They are on this interface rather than a second one because an
	// execution's truth lives on exactly one node, and a publication that
	// could be pointed at a second daemon could seal one node's source and
	// read another's.
	BeginSeal(ctx context.Context, request output.SealRequest) (output.SealStarted, error)
	ConfirmSeal(ctx context.Context,
		confirmation output.SealConfirmation) (output.CaptureAcknowledgement, error)
	InspectSeal(ctx context.Context, handoff output.HandoffID,
		id executioncontrol.Identity) (output.SealStarted, error)
	Canonicalize(ctx context.Context,
		request output.PublicationRequest) (output.CanonicalizationResult, error)
	Publish(ctx context.Context,
		request output.PublicationRequest) (output.PublicationResult, error)
	Attest(ctx context.Context, challenge output.StatChallenge,
		claims output.ReceiptClaims) (output.Receipt, error)
	AcknowledgeRelease(ctx context.Context,
		intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error)
}

// OutputControlClient is the HTTP implementation, bound to one endpoint.
//
// It is bound to an endpoint rather than taking one per call because an
// execution's truth lives on exactly one node: a client that could be pointed
// at a second daemon mid-execution is a client that could ask the wrong node
// whether a process finished.
type OutputControlClient struct {
	endpoint    string
	http        *http.Client
	minter      *executioncontrol.CapabilityMinter
	epoch       executioncontrol.ActivationEpoch
	readTimeout time.Duration
}

// NewOutputControlClient builds the client for one node's daemon.
func NewOutputControlClient(endpoint string, httpClient *http.Client,
	minter *executioncontrol.CapabilityMinter,
	epoch executioncontrol.ActivationEpoch) *OutputControlClient {
	return &OutputControlClient{endpoint: endpoint, http: httpClient, minter: minter, epoch: epoch, readTimeout: output.DefaultOperationTimeout}
}

var _ OutputControl = (*OutputControlClient)(nil)

// MintGrant issues the capture extension's own attenuated capability for one
// operation on one execution.
//
// It is exported because the capture control init container carries one, and
// the pod builder is where it is placed. It is NEVER the base capability: the
// base capability stops and observes an execution, and a capture holding it
// could stop the process it is capturing from.
func (client *OutputControlClient) MintGrant(facet executioncontrol.Facet, operation string,
	id executioncontrol.Identity) (executioncontrol.ControlCapability, error) {
	nonce, err := freshNonce()
	if err != nil {
		return "", err
	}

	return client.minter.Mint(executioncontrol.CapabilityClaims{
		Facet:           facet,
		Operation:       operation,
		Identity:        id,
		ActivationEpoch: client.epoch,
	}, nonce)
}

func freshNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("minting a control capability nonce: %w", err)
	}

	return hex.EncodeToString(raw), nil
}

// call is every request: mint a capability for THIS route's facet and
// operation, present it, decode or fail.
//
// A capability is minted per call and never reused. The daemon refuses a
// replayed nonce inside its TTL, so a client that cached one would work until
// the second call and then fail in a way that looked like a daemon fault.
func (client *OutputControlClient) call(ctx context.Context, facet executioncontrol.Facet,
	operation, path string, id executioncontrol.Identity, body any, into any) error {
	capability, err := client.MintGrant(facet, operation, id)
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding a %s request: %w", operation, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.endpoint+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("building a %s request: %w", operation, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(CapabilityHeaderName, string(capability))

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	defer response.Body.Close()

	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("reading the %s answer: %w", operation, err)
	}
	if response.StatusCode != http.StatusOK {
		// The daemon's refusals are typed and the type is what a caller acts
		// on, so the status is carried rather than flattened into "failed".
		return &OutputControlRefusal{
			Operation: operation,
			Status:    response.StatusCode,
			Body:      string(bytes.TrimSpace(answer)),
		}
	}
	if into == nil {
		return nil
	}
	if err := json.Unmarshal(answer, into); err != nil {
		return fmt.Errorf("decoding the %s answer: %w", operation, err)
	}

	return nil
}

// OutputControlRefusal is a typed refusal from the daemon.
//
// It keeps the status because the difference between 409 (this execution is
// past the point you are asking about) and 403 (your capability does not
// authorize this) and 503 (the daemon cannot read its own ledger) is the
// difference between failing the step, retrying and holding everything -- and a
// caller that saw only "an error" would treat all three the same.
type OutputControlRefusal struct {
	Operation string
	Status    int
	Body      string
}

func (refusal *OutputControlRefusal) Error() string {
	return fmt.Sprintf("the output daemon refused %s with %d: %s",
		refusal.Operation, refusal.Status, refusal.Body)
}

// Unwrap maps the status back onto the leaf sentinel the daemon raised.
//
// The daemon's writeError is the forward direction of this table and this is
// the inverse; without it a caller can only compare integers, and "no seal has
// begun" -- an ordinary answer a coordinator acts on -- is indistinguishable
// from an infrastructure failure by anything but a magic number at the call
// site. The mapping is deliberately COARSE: several sentinels share a status,
// so this recovers the class and the body still names which member it was.
// That is exactly the distinction a caller acts on, and the one that survives
// HTTP.
func (refusal *OutputControlRefusal) Unwrap() error {
	switch refusal.Status {
	case http.StatusForbidden, http.StatusUnauthorized:
		return output.ErrUnauthorized
	case http.StatusNotFound:
		return output.ErrNotFound
	case http.StatusConflict:
		return output.ErrConflict
	case http.StatusPreconditionFailed:
		return output.ErrSealUnconfirmed
	case http.StatusBadRequest:
		return output.ErrIncomplete
	case http.StatusNotAcceptable:
		return output.ErrUnsupportedProtocol
	case http.StatusServiceUnavailable:
		return output.ErrInfrastructure
	}

	return nil
}

// Refused reports whether err is a typed daemon refusal, and what it said.
func Refused(err error) (*OutputControlRefusal, bool) {
	var refusal *OutputControlRefusal
	if ok := asRefusal(err, &refusal); ok {
		return refusal, true
	}

	return nil, false
}

func (client *OutputControlClient) Admit(ctx context.Context,
	envelope executioncontrol.Envelope) (executioncontrol.ClassifyResult, error) {
	var result executioncontrol.ClassifyResult
	err := client.call(ctx, executioncontrol.BaseFacet, "admit", "/execution/v1/admit",
		envelope.Identity, envelope, &result)

	return result, err
}

func (client *OutputControlClient) Classify(ctx context.Context,
	id executioncontrol.Identity) (executioncontrol.ClassifyResult, error) {
	var result executioncontrol.ClassifyResult
	err := client.call(ctx, executioncontrol.BaseFacet, "classify", "/execution/v1/classify", id,
		executioncontrol.ClassifyRequest{
			ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id,
		}, &result)

	return result, err
}

func (client *OutputControlClient) RecordStart(ctx context.Context, id executioncontrol.Identity,
	pod executioncontrol.PodUID,
	process executioncontrol.ProcessIdentity) (executioncontrol.Acknowledgement, error) {
	var ack executioncontrol.Acknowledgement
	err := client.call(ctx, executioncontrol.BaseFacet, "start", "/execution/v1/start", id,
		map[string]any{
			"execution":        id,
			"pod_uid":          pod,
			"process_identity": process,
		}, &ack)

	return ack, err
}

// InspectStart reads the node's stored, signed start, or refuses with
// ErrNotFound when the node recorded none. It writes nothing.
func (client *OutputControlClient) InspectStart(ctx context.Context,
	id executioncontrol.Identity) (executioncontrol.Acknowledgement, error) {
	var ack executioncontrol.Acknowledgement
	err := client.call(ctx, executioncontrol.BaseFacet, "inspect-start", "/execution/v1/start/inspect", id,
		executioncontrol.ClassifyRequest{
			ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id,
		}, &ack)

	return ack, err
}

func (client *OutputControlClient) RecordOutcome(ctx context.Context, id executioncontrol.Identity,
	kind executioncontrol.AcknowledgementKind,
	outcome executioncontrol.ExitOutcome) (executioncontrol.Acknowledgement, error) {
	var ack executioncontrol.Acknowledgement
	err := client.call(ctx, executioncontrol.BaseFacet, "outcome", "/execution/v1/outcome", id,
		map[string]any{"execution": id, "kind": kind, "outcome": outcome}, &ack)

	return ack, err
}

func (client *OutputControlClient) Observe(ctx context.Context, id executioncontrol.Identity,
	wait time.Duration) (executioncontrol.ObserveFinishOrStopResult, error) {
	var result executioncontrol.ObserveFinishOrStopResult
	err := client.call(ctx, executioncontrol.BaseFacet, "observe", "/execution/v1/observe", id,
		executioncontrol.ObserveFinishOrStopRequest{
			ProtocolVersion:  executioncontrol.ProtocolVersion,
			Identity:         id,
			WaitMilliseconds: wait.Milliseconds(),
		}, &result)

	return result, err
}

func (client *OutputControlClient) RequestStop(ctx context.Context,
	id executioncontrol.Identity) (executioncontrol.RequestSourcePreservingStopResult, error) {
	var result executioncontrol.RequestSourcePreservingStopResult
	capability, err := client.MintGrant(executioncontrol.BaseFacet, "stop", id)
	if err != nil {
		return result, err
	}
	err = client.call(ctx, executioncontrol.BaseFacet, "stop", "/execution/v1/stop", id,
		executioncontrol.RequestSourcePreservingStopRequest{
			ProtocolVersion: executioncontrol.ProtocolVersion,
			Identity:        id,
			Capability:      capability,
		}, &result)

	return result, err
}

func (client *OutputControlClient) CleanupEligible(ctx context.Context,
	id executioncontrol.Identity) (executioncontrol.DestructiveCleanupEligibleResult, error) {
	var result executioncontrol.DestructiveCleanupEligibleResult
	err := client.call(ctx, executioncontrol.BaseFacet, "cleanup-eligible",
		"/execution/v1/cleanup-eligible", id,
		executioncontrol.DestructiveCleanupEligibleRequest{
			ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id,
		}, &result)

	return result, err
}

// ReserveIncarnation takes the reservation, and is the only place in the ATC
// that learns a source path.
//
// It answers a directory, and the ATC's whole part is to repeat it into the
// producing Pod's output volume. Req 7 is intact because the daemon derived it:
// the handle generation is that node's monotonic ledger sequence, and no caller
// can guess or choose one. TestNoATCCodeComposesAnIncarnationName fails if any
// file under atc/ starts composing one instead of calling this.
func (client *OutputControlClient) ReserveIncarnation(ctx context.Context,
	admission output.CaptureAdmission) (output.ReservedIncarnation, error) {
	var reserved output.ReservedIncarnation
	if err := client.call(ctx, output.CaptureFacet, "reserve-incarnation",
		"/capture/v1/reserve-incarnation", admission.Execution, admission, &reserved); err != nil {
		return output.ReservedIncarnation{}, err
	}
	// The answer is validated before it is repeated. A reservation whose
	// directory does not derive from the incarnation beside it is not a daemon
	// this ATC should be mounting hostPaths from.
	if err := reserved.Validate(); err != nil {
		return output.ReservedIncarnation{}, fmt.Errorf(
			"the output daemon answered a reservation that does not validate: %w", err)
	}

	return reserved, nil
}

func (client *OutputControlClient) InspectHold(ctx context.Context, id executioncontrol.Identity,
	handoff output.HandoffID) (output.CaptureAcknowledgement, error) {
	var ack output.CaptureAcknowledgement
	err := client.call(ctx, output.CaptureFacet, "inspect-hold", "/capture/v1/hold/inspect", id,
		map[string]any{"execution": id, "handoff_id": handoff}, &ack)

	return ack, err
}

func (client *OutputControlClient) AdmitWriter(ctx context.Context,
	admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	var ack output.CaptureAcknowledgement
	err := client.call(ctx, output.CaptureFacet, "issue-writer-ticket", "/capture/v1/writer-ticket",
		admission.Execution, admission, &ack)

	return ack, err
}

func (client *OutputControlClient) RetireWriter(ctx context.Context,
	admission output.WriterAdmission) (output.CaptureAcknowledgement, error) {
	var ack output.CaptureAcknowledgement
	err := client.call(ctx, output.CaptureFacet, "close-writer-ticket",
		"/capture/v1/writer-ticket/close", admission.Execution, admission, &ack)

	return ack, err
}

func (client *OutputControlClient) InspectWriter(ctx context.Context, id executioncontrol.Identity,
	handoff output.HandoffID, ticket output.WriterTicketID) (output.WriterInspection, error) {
	var answer output.WriterInspection
	err := client.call(ctx, output.CaptureFacet, "inspect-writer-ticket",
		"/capture/v1/writer-ticket/inspect", id, map[string]any{
			"execution": id, "handoff_id": handoff, "writer_ticket_id": ticket,
		}, &answer)
	return answer, err
}

// nodeOutputControls resolves the output daemon for the node an execution
// landed on, and dials it the way the ATC dials the artifact daemon: same
// client certificate, same CA, same scheme predicate. The two daemons are
// separate Pods with separate service accounts and separate buckets -- Req 20
// forbids sharing one -- but the ATC's identity to both is one identity, and a
// second certificate would be a second thing to rotate for no gain.
type nodeOutputControls struct {
	config   Config
	resolver *NodeIPResolver
	minter   *executioncontrol.CapabilityMinter
	epoch    executioncontrol.ActivationEpoch
}

// NewOutputControls builds the resolver a Worker is given.
func NewOutputControls(config Config, resolver *NodeIPResolver,
	minter *executioncontrol.CapabilityMinter,
	epoch executioncontrol.ActivationEpoch) OutputControlResolver {
	return &nodeOutputControls{config: config, resolver: resolver, minter: minter, epoch: epoch}
}

func (controls *nodeOutputControls) ForNode(ctx context.Context, nodeName string) (OutputControl, error) {
	return controls.clientForNode(ctx, nodeName)
}

func (controls *nodeOutputControls) clientForNode(ctx context.Context, nodeName string) (*OutputControlClient, error) {
	if controls.resolver == nil || nodeName == "" {
		return nil, fmt.Errorf("no node to reach the output daemon on")
	}
	nodeIP, err := controls.resolver.Resolve(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("resolving node %s: %w", nodeName, err)
	}
	port := controls.config.OutputDaemonPort
	if port == 0 {
		port = DefaultOutputDaemonPort
	}

	// The OUTPUT plane's scheme and the OUTPUT plane's client. Not the
	// artifact daemon's: that predicate is a switch on a different daemon
	// serving a different bucket under a different identity, and its client
	// certificate is issued by a CA this daemon does not trust.
	client := NewOutputControlClient(
		fmt.Sprintf("%s://%s:%d", outputDaemonURLScheme(), nodeIP, port),
		newOutputDaemonHTTPClient(controls.config, 30*time.Second),
		controls.minter, controls.epoch)
	client.readTimeout = controls.config.OutputOperationTimeout
	return client, nil
}

// The publication half, which Phase 5's coordinator drives.
//
// These are the ATC-side calls of the capture extension: fence writer
// admission, prove the drain, learn what the sealed tree is, create the object,
// obtain a per-capture receipt, and release the source. Each is one route and
// one attenuated grant, minted per call like every other.
//
// They are on the SAME client as the hold and the writer ticket because an
// execution's truth lives on exactly one node: a publication that could be
// pointed at a second daemon is a publication that could seal one node's source
// and read another's.

func (client *OutputControlClient) BeginSeal(ctx context.Context,
	request output.SealRequest) (output.SealStarted, error) {
	var started output.SealStarted
	err := client.call(ctx, output.CaptureFacet, "begin-seal", "/capture/v1/seal",
		request.Execution, request, &started)

	return started, err
}

// ConfirmSeal sends the route's own body rather than the value type.
//
// output.SealConfirmation carries no json tags -- it is a control-plane value
// and never a wire type -- and the route takes the execution alongside the
// statement so that a capability minted for one execution cannot confirm
// another's seal. Marshalling the value directly produced Go field names and
// no execution at all, which the daemon correctly refused as an empty identity.
func (client *OutputControlClient) ConfirmSeal(ctx context.Context,
	confirmation output.SealConfirmation) (output.CaptureAcknowledgement, error) {
	execution := confirmation.Started.Acknowledgement.Execution

	var ack output.CaptureAcknowledgement
	err := client.call(ctx, output.CaptureFacet, "confirm-seal", "/capture/v1/seal/confirm",
		execution, map[string]any{
			"execution":     execution,
			"started":       confirmation.Started,
			"drained":       confirmation.Drained,
			"capture_fence": confirmation.CaptureFence,
			"observed_at":   confirmation.ObservedAt,
		}, &ack)

	return ack, err
}

func (client *OutputControlClient) InspectSeal(ctx context.Context, handoff output.HandoffID,
	id executioncontrol.Identity) (output.SealStarted, error) {
	var started output.SealStarted
	err := client.call(ctx, output.CaptureFacet, "inspect-seal", "/capture/v1/seal/inspect", id,
		map[string]any{"execution": id, "handoff_id": handoff}, &started)

	return started, err
}

// Canonicalize answers what the sealed tree is and creates nothing.
//
// It is a separate call from Publish because requirement 21 puts a durable
// logical resolution between them: every possibly-created object has to have a
// pre-existing reservation that recovery and inventory can correlate.
func (client *OutputControlClient) Canonicalize(ctx context.Context,
	request output.PublicationRequest) (output.CanonicalizationResult, error) {
	var result output.CanonicalizationResult
	err := client.call(ctx, output.CaptureFacet, "canonicalize", "/capture/v1/canonicalize",
		request.Execution, request, &result)

	return result, err
}

func (client *OutputControlClient) Publish(ctx context.Context,
	request output.PublicationRequest) (output.PublicationResult, error) {
	var result output.PublicationResult
	err := client.call(ctx, output.CaptureFacet, "publish", "/capture/v1/publish",
		request.Execution, request, &result)

	return result, err
}

// Attest answers a one-use challenge with a signed per-capture receipt.
//
// The challenge names the generation the publish assigned, so this cannot be
// folded into Publish: a receipt signed inside the publish would be bound to no
// challenge and would answer every later one naming the same facts.
func (client *OutputControlClient) Attest(ctx context.Context, challenge output.StatChallenge,
	claims output.ReceiptClaims) (output.Receipt, error) {
	var receipt output.Receipt
	err := client.call(ctx, output.CaptureFacet, "stat", "/capture/v1/stat", claims.Execution,
		map[string]any{
			"execution": claims.Execution,
			"challenge": challenge,
			"claims":    claims,
		}, &receipt)

	return receipt, err
}

func (client *OutputControlClient) AcknowledgeRelease(ctx context.Context,
	intent output.ReleaseIntent) (output.ReleaseAcknowledgement, error) {
	var ack output.ReleaseAcknowledgement
	err := client.call(ctx, output.CaptureFacet, "release-hold", "/capture/v1/release",
		intent.Execution, intent, &ack)

	return ack, err
}
