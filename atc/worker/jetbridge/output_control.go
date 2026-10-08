package jetbridge

// The ATC's client for the artifact daemon's output-plane control API.
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
// supervisor writes go through the same client as the capture extension's seal,
// publish, release and stat, because there must be exactly one owner of an execution's
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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// OutputControl is what the runtime needs from a node's output plane.
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

	// The capture routes, which the control plane's capture coordinator
	// drives. They are on this interface rather than a second one because an
	// execution's truth lives on exactly one node.
	output.SourceControl
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
	readTimeout time.Duration
	// node is the UID of the node this client's daemon runs on, when the
	// client was chosen for one: a read warrant is bound to it.
	node executioncontrol.NodeUID
}

// NodeUID is the node this client reads from, or "" when it was not chosen
// for a node.
func (client *OutputControlClient) NodeUID() executioncontrol.NodeUID { return client.node }

// OnNode records which node this client's daemon runs on, for a caller that
// built the client for a known node rather than choosing one.
func (client *OutputControlClient) OnNode(node executioncontrol.NodeUID) *OutputControlClient {
	client.node = node
	return client
}

// NewOutputControlClient builds the client for one node's daemon.
func NewOutputControlClient(endpoint string, httpClient *http.Client,
	minter *executioncontrol.CapabilityMinter) *OutputControlClient {
	return &OutputControlClient{endpoint: endpoint, http: httpClient, minter: minter, readTimeout: output.DefaultOperationTimeout}
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
		Facet:     facet,
		Operation: operation,
		Identity:  id,
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
	return fmt.Sprintf("the output plane refused %s with %d: %s",
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
	case http.StatusAccepted:
		// Seal or publish, still running on the node; the body says which.
		if strings.Contains(refusal.Body, `"publishing"`) {
			return output.ErrPublishInProgress
		}
		return output.ErrSealInProgress
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

// InspectStart reads the node's stored start, or refuses with
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

// nodeOutputControls resolves the output plane for the node an execution
// landed on. It is served by that node's artifact daemon, so it is dialled
// with the same client certificate, CA and scheme predicate as every other
// artifact daemon call.
type nodeOutputControls struct {
	config   Config
	resolver *NodeIPResolver
	minter   *executioncontrol.CapabilityMinter
}

// NewOutputControls builds the resolver a Worker is given.
func NewOutputControls(config Config, resolver *NodeIPResolver,
	minter *executioncontrol.CapabilityMinter) OutputControlResolver {
	return &nodeOutputControls{config: config, resolver: resolver, minter: minter}
}

func (controls *nodeOutputControls) ForNode(ctx context.Context, nodeName string) (OutputControl, error) {
	return controls.clientForNode(ctx, nodeName)
}

func (controls *nodeOutputControls) clientForNode(ctx context.Context, nodeName string) (*OutputControlClient, error) {
	if controls.resolver == nil || nodeName == "" {
		return nil, fmt.Errorf("no node to reach the output plane on")
	}
	nodeIP, err := controls.resolver.Resolve(ctx, nodeName)
	if err != nil {
		return nil, fmt.Errorf("resolving node %s: %w", nodeName, err)
	}
	port := controls.config.ArtifactDaemonPort
	if port == 0 {
		port = defaultArtifactDaemonPort
	}

	// The output plane is mounted in the artifact daemon: one endpoint, one
	// TLS configuration, one client certificate.
	client := NewOutputControlClient(
		fmt.Sprintf("%s://%s:%d", outputPlaneURLScheme(), nodeIP, port),
		newOutputPlaneHTTPClient(controls.config, 30*time.Second),
		controls.minter)
	client.readTimeout = controls.config.OutputOperationTimeout
	return client, nil
}

// The capture routes, which the capture coordinator drives: seal, publish,
// release and stat. Each is one route and one attenuated grant, minted per
// call like every other, on the same client as the base protocol, because an
// execution's truth lives on exactly one node.

func (client *OutputControlClient) Seal(ctx context.Context,
	request output.CaptureSealRequest) (output.CaptureSealResult, error) {
	var result output.CaptureSealResult
	err := client.call(ctx, output.CaptureFacet, "seal", "/capture/v1/seal",
		request.Execution, request, &result)

	return result, err
}

func (client *OutputControlClient) Publish(ctx context.Context,
	request output.CapturePublishRequest) (output.CapturePublishResult, error) {
	var result output.CapturePublishResult
	err := client.call(ctx, output.CaptureFacet, "publish", "/capture/v1/publish",
		request.Execution, request, &result)

	return result, err
}

func (client *OutputControlClient) Release(ctx context.Context,
	request output.CaptureReleaseRequest) (output.CaptureReleaseAcknowledgement, error) {
	var ack output.CaptureReleaseAcknowledgement
	err := client.call(ctx, output.CaptureFacet, "release", "/capture/v1/release",
		request.Execution, request, &ack)

	return ack, err
}

func (client *OutputControlClient) Stat(ctx context.Context,
	request output.CaptureStatRequest) (output.CapturePublishResult, error) {
	var result output.CapturePublishResult
	err := client.call(ctx, output.CaptureFacet, "stat", "/capture/v1/stat",
		request.Execution, request, &result)

	return result, err
}

// outputPlaneURLScheme is the scheme every ATC-side caller of the output plane
// uses. The output plane is TLS-only: ValidateOutputPlaneTLS refuses at startup
// an output plane over an artifact daemon the ATC does not speak mTLS to, so
// there is no configuration under which the answer is "http".
func outputPlaneURLScheme() string {
	return "https"
}

// ValidateOutputPlaneTLS refuses at STARTUP an output plane whose artifact
// daemon client credential is missing or partial. The plane's off-node routes
// refuse every request that carries no verified client certificate, so an ATC
// without one would fail at the first capture rather than at startup.
func ValidateOutputPlaneTLS(certPath, keyPath, caCertPath string) error {
	if DaemonTLSConfigured(certPath, keyPath, caCertPath) {
		return nil
	}
	if err := ValidateDaemonTLSFlags(certPath, keyPath, caCertPath); err != nil {
		return err
	}

	return errors.New("the Hangar output plane is enabled and the artifact daemon's TLS is not " +
		"configured. The output plane is served by the artifact daemon and is TLS-only: its " +
		"off-node routes refuse every request that carries no verified client certificate, so " +
		"--kubernetes-artifact-daemon-tls-cert, -tls-key and -tls-ca-cert are required")
}

// newOutputPlaneHTTPClient returns the *http.Client the ATC calls the output
// plane with: the artifact daemon's client certificate, CA and server name.
func newOutputPlaneHTTPClient(cfg Config, timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if triple := wireTLS(cfg); triple.Configured() {
		tlsConfig, err := triple.ClientConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"WARNING: output plane mTLS: %v — every control call will be refused\n", err)
		} else {
			transport.TLSClientConfig = tlsConfig
		}
	}

	return &http.Client{Timeout: timeout, Transport: transport}
}
