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
}

// OutputControlClient is the HTTP implementation, bound to one endpoint.
//
// It is bound to an endpoint rather than taking one per call because an
// execution's truth lives on exactly one node: a client that could be pointed
// at a second daemon mid-execution is a client that could ask the wrong node
// whether a process finished.
type OutputControlClient struct {
	endpoint string
	http     *http.Client
	minter   *executioncontrol.CapabilityMinter
	epoch    executioncontrol.ActivationEpoch
}

// NewOutputControlClient builds the client for one node's daemon.
func NewOutputControlClient(endpoint string, httpClient *http.Client,
	minter *executioncontrol.CapabilityMinter,
	epoch executioncontrol.ActivationEpoch) *OutputControlClient {
	return &OutputControlClient{endpoint: endpoint, http: httpClient, minter: minter, epoch: epoch}
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

	return NewOutputControlClient(
		fmt.Sprintf("%s://%s:%d", daemonURLScheme(controls.config), nodeIP, port),
		newDaemonHTTPClient(controls.config, 30*time.Second),
		controls.minter, controls.epoch), nil
}
